package agentd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestAgentPersistsVerifiableTrajectory(t *testing.T) {
	chatModel := &fakeModel{responses: []*schema.Message{schema.AssistantMessage("done", nil)}}
	agent := newTestAgent(t, chatModel, nil)
	agent.modelName = "test-model"
	var captured []TrajectoryRecord
	runID, err := agent.RunWithOptions(context.Background(), "trajectory", "build", true, func(event Event) error {
		if event.Type == EventTrajectoryRecord {
			data, marshalErr := json.Marshal(event.Payload)
			require.NoError(t, marshalErr)
			var record TrajectoryRecord
			require.NoError(t, json.Unmarshal(data, &record))
			captured = append(captured, record)
		}
		return nil
	})
	require.NoError(t, err)
	require.Len(t, captured, 4)
	require.Equal(t, []string{TrajectoryRunStarted, TrajectoryModelInput, TrajectoryModelOutput, TrajectoryRunFinished}, []string{captured[0].Type, captured[1].Type, captured[2].Type, captured[3].Type})
	var started trajectoryRunStarted
	require.NoError(t, json.Unmarshal(captured[0].Payload, &started))
	require.Equal(t, agent.prompt, started.SystemPrompt)

	records, err := agent.trajectory.Records(runID)
	require.NoError(t, err)
	require.Equal(t, captured, records)
	for index := range records {
		require.Equal(t, int64(index+1), records[index].Sequence)
		if index > 0 {
			require.Equal(t, records[index-1].Hash, records[index].PreviousHash)
		}
	}

	path := filepath.Join(agent.trajectory.root, runID+".jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	data = bytes.Replace(data, []byte(`"type":"model.output"`), []byte(`"type":"model.tampered"`), 1)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, err = NewTrajectoryStore(filepath.Dir(filepath.Dir(agent.trajectory.root))).Records(runID)
	require.ErrorContains(t, err, "hash is invalid")
}

func TestDecisionReplayInjectsRecordedToolResults(t *testing.T) {
	echo, err := toolutils.InferTool("echo", "echo", func(_ context.Context, input struct {
		Text string `json:"text"`
	}) (string, error) {
		return input.Text, nil
	})
	require.NoError(t, err)
	source := &fakeModel{responses: []*schema.Message{
		toolCallMessage("call-source", "echo", `{"text":"ok"}`),
		schema.AssistantMessage("source answer", nil),
	}}
	agent := newTestAgent(t, source, []tool.BaseTool{echo})
	runID, err := agent.Run(context.Background(), "decision-replay", "start", func(Event) error { return nil })
	require.NoError(t, err)
	records, err := agent.trajectory.Records(runID)
	require.NoError(t, err)

	source.responses = []*schema.Message{
		toolCallMessage("call-replay", "echo", `{ "text": "ok" }`),
		schema.AssistantMessage("changed answer", nil),
	}
	report, err := replayDecisions(context.Background(), agent.model, agent.prompt, records)
	require.NoError(t, err)
	require.Equal(t, 2, report.TotalSteps)
	require.Equal(t, 2, report.MatchedSteps)
	require.Equal(t, 1.0, report.Score)
	require.True(t, report.Steps[1].ContentChanged)
}

func TestDecisionReplayUsesCurrentPromptAndKeepsMemory(t *testing.T) {
	records := []TrajectoryRecord{
		{Type: TrajectoryRunStarted, Payload: mustJSON(t, trajectoryRunStarted{SystemPrompt: "old prompt"})},
		{Type: TrajectoryModelInput, Step: 1, Payload: mustJSON(t, trajectoryModelInput{Messages: []*schema.Message{
			schema.SystemMessage("old prompt\n\nProject memory:\nkeep this"),
			schema.UserMessage("start"),
		}})},
		{Type: TrajectoryModelOutput, Step: 1, Payload: mustJSON(t, trajectoryModelOutput{Message: schema.AssistantMessage("done", nil)})},
	}
	chatModel := &fakeModel{responses: []*schema.Message{schema.AssistantMessage("done", nil)}}
	report, err := replayDecisions(context.Background(), chatModel, "new prompt", records)
	require.NoError(t, err)
	require.Equal(t, 1, report.MatchedSteps)
	require.Equal(t, "new prompt\n\nProject memory:\nkeep this", chatModel.requests[0][0].Content)
}

func TestLiveReplayExecutesToolsAgainstRestoredWorkspace(t *testing.T) {
	workspace := t.TempDir()
	skills, err := LoadSkills("", workspace)
	require.NoError(t, err)
	tools, _, err := newLocalTools(workspace, skills)
	require.NoError(t, err)
	memory := NewMemoryStore(workspace, 128000)
	source := &fakeModel{responses: []*schema.Message{
		toolCallMessage("write-source", "write_file", `{"path":"result.txt","content":"source"}`),
		schema.AssistantMessage("source done", nil),
	}}
	agent, err := NewAgent(context.Background(), source, tools, memory, "test prompt")
	require.NoError(t, err)
	snapshot, err := createWorkspaceSnapshot(workspace)
	require.NoError(t, err)
	runID, err := agent.Run(context.Background(), "live-replay", "write result", func(Event) error { return nil })
	require.NoError(t, err)
	records, err := agent.trajectory.Records(runID)
	require.NoError(t, err)
	require.NoError(t, restoreWorkspaceSnapshot(workspace, snapshot))

	source.responses = []*schema.Message{
		toolCallMessage("write-replay", "write_file", `{"path":"result.txt","content":"replayed"}`),
		schema.AssistantMessage("replay done", nil),
	}
	report, err := replayLive(context.Background(), agent, records)
	require.NoError(t, err)
	require.Equal(t, "completed", report.Status)
	require.Equal(t, 2, report.TotalSteps)
	require.Equal(t, 1, report.MatchedSteps)
	require.Equal(t, "replay done", report.Output)
	content, err := os.ReadFile(filepath.Join(workspace, "result.txt"))
	require.NoError(t, err)
	require.Equal(t, "replayed", string(content))
}

func TestWorkspaceSnapshotExcludesRuntimeStateAndRejectsTraversal(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "app.txt"), []byte("source"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".agentland"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".agentland", "state"), []byte("keep"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "node_modules", "dep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "node_modules", "dep", "index.js"), []byte("ignored"), 0o644))
	snapshot, err := createWorkspaceSnapshot(workspace)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "stale.txt"), []byte("stale"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "src", "app.txt"), []byte("changed"), 0o644))
	require.NoError(t, restoreWorkspaceSnapshot(workspace, snapshot))
	restoredSnapshot, err := createWorkspaceSnapshot(workspace)
	require.NoError(t, err)
	require.Equal(t, snapshot, restoredSnapshot)
	content, err := os.ReadFile(filepath.Join(workspace, "src", "app.txt"))
	require.NoError(t, err)
	require.Equal(t, "source", string(content))
	_, err = os.Stat(filepath.Join(workspace, "stale.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	internal, err := os.ReadFile(filepath.Join(workspace, ".agentland", "state"))
	require.NoError(t, err)
	require.Equal(t, "keep", string(internal))
	_, err = os.Stat(filepath.Join(workspace, "node_modules"))
	require.ErrorIs(t, err, os.ErrNotExist)

	malicious := snapshotArchive(t, "../outside.txt", "escaped")
	err = restoreWorkspaceSnapshot(workspace, malicious)
	require.ErrorContains(t, err, "escapes workspace")
}

func toolCallMessage(id, name, arguments string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}})
}

func snapshotArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	require.NoError(t, tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
	_, err := tarWriter.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())
	return output.Bytes()
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
