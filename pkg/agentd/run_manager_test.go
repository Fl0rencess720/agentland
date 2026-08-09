package agentd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestRunManagerContinuesAfterSubscriberDisconnectAndReplaysEvents(t *testing.T) {
	workspace := t.TempDir()
	model := &fakeModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID: "shell-1", Type: "function",
			Function: schema.FunctionCall{Name: "shell", Arguments: `{"command":"sleep 0.1; printf done > recovered.txt"}`},
		}}),
		schema.AssistantMessage("finished", nil),
	}}
	server, err := newServer(context.Background(), &Config{WorkspaceRoot: workspace, ContextTokens: 128000}, model)
	require.NoError(t, err)
	defer server.Close()

	state, created, err := server.runs.Start("run-async", "conversation", "build", true)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, runStatusRunning, state.Status)

	subscription, cancel := context.WithCancel(context.Background())
	received := make(chan Event, 1)
	done := make(chan error, 1)
	go func() {
		done <- server.runs.Subscribe(subscription, state.RunID, 0, func(event Event) error {
			select {
			case received <- event:
			default:
			}
			cancel()
			return nil
		}, nil)
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("run did not emit its first event")
	}
	require.ErrorIs(t, <-done, context.Canceled)

	require.Eventually(t, func() bool {
		current, ok := server.runs.Get(state.RunID)
		return ok && current.Status == runStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
	content, err := os.ReadFile(filepath.Join(workspace, "recovered.txt"))
	require.NoError(t, err)
	require.Equal(t, "done", string(content))

	replayed := make([]Event, 0)
	require.NoError(t, server.runs.Subscribe(context.Background(), state.RunID, 0, func(event Event) error {
		replayed = append(replayed, event)
		return nil
	}, nil))
	require.NotEmpty(t, replayed)
	require.Equal(t, EventRunCompleted, replayed[len(replayed)-1].Type)

	duplicate, created, err := server.runs.Start("run-async", "conversation", "build", true)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, runStatusCompleted, duplicate.Status)
}

func TestRunManagerRestoresCompletedRunJournalAfterRestart(t *testing.T) {
	workspace := t.TempDir()
	server, err := newServer(context.Background(), &Config{WorkspaceRoot: workspace, ContextTokens: 128000}, &fakeModel{
		responses: []*schema.Message{schema.AssistantMessage("finished", nil)},
	})
	require.NoError(t, err)
	state, created, err := server.runs.Start("run-persisted", "conversation", "build", false)
	require.NoError(t, err)
	require.True(t, created)
	require.Eventually(t, func() bool {
		current, ok := server.runs.Get(state.RunID)
		return ok && current.Status == runStatusCompleted
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, server.Close())

	restarted, err := newServer(context.Background(), &Config{WorkspaceRoot: workspace, ContextTokens: 128000}, &fakeModel{})
	require.NoError(t, err)
	defer restarted.Close()
	restored, ok := restarted.runs.Get(state.RunID)
	require.True(t, ok)
	require.Equal(t, runStatusCompleted, restored.Status)
	require.Positive(t, restored.LastSequence)

	events := make([]Event, 0)
	require.NoError(t, restarted.runs.Subscribe(context.Background(), state.RunID, 0, func(event Event) error {
		events = append(events, event)
		return nil
	}, nil))
	require.NotEmpty(t, events)
	require.Equal(t, EventRunCompleted, events[len(events)-1].Type)
	duplicate, created, err := restarted.runs.Start(state.RunID, "conversation", "build", false)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, runStatusCompleted, duplicate.Status)
}
