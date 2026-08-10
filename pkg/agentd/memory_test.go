package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestMemoryStorePersistsMessagesAndIgnoresDamagedTail(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	require.NoError(t, store.Append("main", schema.UserMessage("hello")))
	require.NoError(t, store.Append("main", schema.AssistantMessage("world", nil)))

	history := filepath.Join(workspace, ".agentland", "conversations", "main", "history.jsonl")
	f, err := os.OpenFile(history, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"role":`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	messages, err := store.Messages("main")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, "hello", messages[0].Content)
	require.Equal(t, "world", messages[1].Content)
}

func TestMemoryStoreRejectsMalformedTerminatedTail(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	require.NoError(t, store.Append("main", schema.UserMessage("hello")))

	history := filepath.Join(workspace, ".agentland", "conversations", "main", "history.jsonl")
	f, err := os.OpenFile(history, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("{\"role\":\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = store.Messages("main")
	require.ErrorContains(t, err, "decode conversation history line 2")
	_, err = store.Context(context.Background(), "main", "system", &fakeModel{})
	require.ErrorContains(t, err, "decode conversation history line 2")
}

func TestMemoryStoreCompactsOldMessages(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 40)
	for i := 0; i < 4; i++ {
		require.NoError(t, store.Append("main", schema.UserMessage("a long user request that fills the context")))
		require.NoError(t, store.Append("main", schema.AssistantMessage("a long assistant response that fills the context", nil)))
	}
	chatModel := &fakeModel{}
	messages, err := store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(messages), 4)
	require.Contains(t, messages[1].Content, "Conversation state snapshot")
	require.Equal(t, 2, chatModel.generateCalls)
	summaryPath := filepath.Join(workspace, ".agentland", "conversations", "main", "summary.json")
	_, err = os.Stat(summaryPath)
	require.NoError(t, err)
	summary, err := store.loadSummary("main")
	require.NoError(t, err)
	require.Positive(t, summary.UpTo)
	require.Positive(t, summary.Offset)
}

func TestMemoryCompressionStartsAtHalfWindowAndKeepsRecentThirtyPercent(t *testing.T) {
	store := NewMemoryStore(t.TempDir(), 100)
	chatModel := &fakeModel{}
	require.NoError(t, store.Append("main", schema.UserMessage(strings.Repeat("a", 100))))
	_, err := store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.Zero(t, chatModel.generateCalls)

	require.NoError(t, store.Append("main", schema.AssistantMessage(strings.Repeat("b", 100), nil)))
	_, err = store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.Equal(t, 2, chatModel.generateCalls)

	history := make([]*schema.Message, 10)
	for index := range history {
		history[index] = schema.UserMessage(strings.Repeat("x", 20))
	}
	require.Equal(t, 7, compressionSplitPoint(history, compressionPreservePercent))
}

func TestMemoryCompressionUsesVerifiedStateSnapshot(t *testing.T) {
	store := NewMemoryStore(t.TempDir(), 40, "summary-model")
	for i := 0; i < 4; i++ {
		require.NoError(t, store.Append("main", schema.UserMessage("implement the requested application change")))
		require.NoError(t, store.Append("main", schema.AssistantMessage("completed part of the implementation", nil)))
	}
	chatModel := &fakeModel{generateResponses: []*schema.Message{
		schema.AssistantMessage("draft snapshot", nil),
		schema.AssistantMessage("<state_snapshot>verified snapshot</state_snapshot>", nil),
	}}

	messages, err := store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.Equal(t, 2, chatModel.generateCalls)
	require.Contains(t, messages[1].Content, "verified snapshot")
	require.Contains(t, chatModel.generateRequests[0][0].Content, "state snapshot")
	require.Contains(t, chatModel.generateRequests[1][len(chatModel.generateRequests[1])-1].Content, "Check the state snapshot")
}

func TestMemoryCompressionDegradesOldToolOutputsAndKeepsArtifacts(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	oldOutput := strings.Repeat("old output line\n", 16_000)
	recentOutput := strings.Repeat("recent output\n", 100)
	messages := []*schema.Message{
		schema.ToolMessage(oldOutput, "old-call", schema.WithToolName("shell")),
		schema.ToolMessage(recentOutput, "recent-call", schema.WithToolName("shell")),
	}

	degraded, err := store.degradeToolOutputs("main", messages)
	require.NoError(t, err)
	require.Contains(t, degraded[0].Content, "Full output: .agentland/logs/compression/main/")
	require.Equal(t, recentOutput, degraded[1].Content)
	require.Equal(t, oldOutput, messages[0].Content)

	artifacts, err := filepath.Glob(filepath.Join(workspace, ".agentland", "logs", "compression", "main", "*.log"))
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	content, err := os.ReadFile(artifacts[0])
	require.NoError(t, err)
	require.Equal(t, oldOutput, string(content))
}

func TestMemoryCompressionFailureFallsBackWithoutRetryLoop(t *testing.T) {
	store := NewMemoryStore(t.TempDir(), 40)
	for i := 0; i < 4; i++ {
		require.NoError(t, store.Append("main", schema.UserMessage("a long user request that fills the context")))
		require.NoError(t, store.Append("main", schema.AssistantMessage("a long assistant response that fills the context", nil)))
	}
	chatModel := &fakeModel{generateErr: errors.New("compressor unavailable")}

	messages, err := store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.NotEmpty(t, messages)
	require.Equal(t, 1, chatModel.generateCalls)
	_, err = store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.Equal(t, 1, chatModel.generateCalls)
}

func TestCompressionSplitPointKeepsToolCallAndResultTogether(t *testing.T) {
	history := []*schema.Message{
		schema.UserMessage("start"),
		toolCallMessage("call-1", "shell", `{"command":"first"}`),
		schema.ToolMessage(strings.Repeat("first result ", 100), "call-1", schema.WithToolName("shell")),
		toolCallMessage("call-2", "shell", `{"command":"second"}`),
		schema.ToolMessage(strings.Repeat("second result ", 100), "call-2", schema.WithToolName("shell")),
	}

	split := compressionSplitPoint(history, compressionPreservePercent)
	require.Positive(t, split)
	require.Less(t, split, len(history))
	require.True(t, isCompressionBoundary(history[split]))
	require.NotEqual(t, schema.Tool, history[split].Role)
}

func TestMemoryContextSeeksPastSummarizedHistory(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	dir, err := store.conversationDir("main")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	prefix := strings.Repeat("x", maxHistoryLineBytes+1) + "\n"
	tail, err := json.Marshal(schema.UserMessage("recent"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "history.jsonl"), append([]byte(prefix), append(tail, '\n')...), 0o600))
	require.NoError(t, store.saveSummary("main", &conversationSummary{
		UpTo:      1,
		Offset:    int64(len(prefix)),
		Content:   "old history",
		UpdatedAt: "now",
	}))

	messages, err := store.Context(context.Background(), "main", "system", &fakeModel{})
	require.NoError(t, err)
	require.Len(t, messages, 4)
	require.Equal(t, "recent", messages[3].Content)
}

func TestMemoryContextRejectsOversizedUnsummarizedLine(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	dir, err := store.conversationDir("main")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "history.jsonl"),
		[]byte(strings.Repeat("x", maxHistoryLineBytes+1)+"\n"),
		0o600,
	))

	_, err = store.Context(context.Background(), "main", "system", &fakeModel{})
	require.ErrorContains(t, err, "history line exceeds")
}

func TestMemoryContextRejectsOversizedProjectMemoryAndSummary(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".agentland"), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, ".agentland", "MEMORY.md"),
		[]byte(strings.Repeat("m", maxProjectMemoryBytes+1)),
		0o600,
	))
	_, err := store.Context(context.Background(), "main", "system", &fakeModel{})
	require.ErrorContains(t, err, "file exceeds")

	require.NoError(t, os.Remove(filepath.Join(workspace, ".agentland", "MEMORY.md")))
	dir, err := store.conversationDir("main")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "summary.json"),
		[]byte(strings.Repeat("s", maxSummaryFileBytes+1)),
		0o600,
	))
	_, err = store.Context(context.Background(), "main", "system", &fakeModel{})
	require.ErrorContains(t, err, "file exceeds")
}

func TestMemoryMessagesStopsAtAggregateLimit(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	dir, err := store.conversationDir("main")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	message, err := json.Marshal(schema.UserMessage(strings.Repeat("x", maxHistoryLineBytes/2)))
	require.NoError(t, err)
	history := bytesOfLines(message, maxReturnedHistoryBytes/len(message)+2)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "history.jsonl"), history, 0o600))

	_, err = store.Messages("main")
	require.ErrorContains(t, err, "conversation history exceeds")
}

func TestProgressiveContextRunsFiveStagesInOrder(t *testing.T) {
	store := NewMemoryStore(t.TempDir(), 128000)
	messages := make([]*schema.Message, 0, 24)
	largeBudgetOutput := strings.Repeat("budget output\n", 20_000)
	microOutput := strings.Repeat("refetchable file content ", 500)
	messages = append(messages,
		toolCallMessage("budget-call", "shell", `{"command":"build"}`),
		schema.ToolMessage(largeBudgetOutput, "budget-call", schema.WithToolName("shell")),
		toolCallMessage("micro-call", "read_file", `{"path":"large.txt"}`),
		schema.ToolMessage(microOutput, "micro-call", schema.WithToolName("read_file")),
	)
	for index := 0; index < 8; index++ {
		messages = append(messages, schema.UserMessage(strings.Repeat("requirement ", 80)))
		assistant := schema.AssistantMessage(strings.Repeat("implementation detail ", 60), nil)
		assistant.ReasoningContent = strings.Repeat("private reasoning ", 300)
		messages = append(messages, assistant)
	}
	messages = append(messages,
		schema.UserMessage(strings.Repeat("recent request ", 80)),
		schema.AssistantMessage(strings.Repeat("recent answer ", 80), nil),
	)
	chatModel := &fakeModel{generateResponses: []*schema.Message{
		schema.AssistantMessage(strings.Repeat("large collapse ", 500), nil),
		schema.AssistantMessage("draft state", nil),
		schema.AssistantMessage("<state_snapshot>verified state</state_snapshot>", nil),
	}}

	result, summary, collapse, summaryChanged, collapseChanged, reports, err := store.runProgressiveContext(
		context.Background(), "main", chatModel, nil, nil, testContextEntries(messages), 300,
	)
	require.NoError(t, err)
	require.Equal(t, progressiveContextStageOrder, stageNames(reports))
	for _, report := range reports {
		require.Truef(t, report.Triggered, "stage %s did not trigger", report.Name)
	}
	require.True(t, summaryChanged)
	require.True(t, collapseChanged)
	require.NotNil(t, summary)
	require.Nil(t, collapse)
	require.NotEmpty(t, result)
	requireToolProtocolValid(t, contextMessages(result))
	require.Equal(t, 3, chatModel.generateCalls)
}

func TestToolResultBudgetPersistsFullOutputAndBoundsContextView(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	large := strings.Repeat("0123456789abcdef", 20_000)
	messages := []*schema.Message{
		toolCallMessage("call-1", "shell", `{"command":"large"}`),
		schema.ToolMessage(large, "call-1", schema.WithToolName("shell")),
	}

	result, err := store.applyToolResultBudget("main", testContextEntries(messages))
	require.NoError(t, err)
	require.LessOrEqual(t, estimateTextTokens(result[1].message.Content), compressionSingleToolTokens)
	require.Contains(t, result[1].message.Content, ".agentland/logs/compression/main/")
	require.Equal(t, large, messages[1].Content)

	artifacts, err := filepath.Glob(filepath.Join(workspace, ".agentland", "logs", "compression", "main", "*.log"))
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	content, err := os.ReadFile(artifacts[0])
	require.NoError(t, err)
	require.Equal(t, large, string(content))

	cumulative := make([]*schema.Message, 0, 240)
	medium := strings.Repeat("medium-output ", 2_400)
	for index := 0; index < 120; index++ {
		callID := fmt.Sprintf("medium-%d", index)
		cumulative = append(cumulative,
			toolCallMessage(callID, "shell", `{"command":"medium"}`),
			schema.ToolMessage(medium, callID, schema.WithToolName("shell")),
		)
	}
	bounded, err := store.applyToolResultBudget("main", testContextEntries(cumulative))
	require.NoError(t, err)
	require.Contains(t, bounded[1].message.Content, ".agentland/logs/compression/main/")
	require.Equal(t, medium, bounded[len(bounded)-1].message.Content)
	totalToolTokens, archived := 0, 0
	for _, entry := range bounded {
		if entry.message.Role != schema.Tool {
			continue
		}
		tokens := estimateTextTokens(entry.message.Content)
		require.LessOrEqual(t, tokens, compressionSingleToolTokens)
		totalToolTokens += tokens
		if strings.Contains(entry.message.Content, "Full output:") {
			archived++
		}
	}
	require.LessOrEqual(t, totalToolTokens, compressionToolTokenBudget)
	require.Positive(t, archived)
	requireToolProtocolValid(t, contextMessages(bounded))

	invalid := strings.Repeat(string([]byte{0xff, 'x'}), 40_000)
	invalidResult, err := store.applyToolResultBudget("main", testContextEntries([]*schema.Message{
		toolCallMessage("invalid", "shell", `{"command":"invalid"}`),
		schema.ToolMessage(invalid, "invalid", schema.WithToolName("shell")),
	}))
	require.NoError(t, err)
	require.True(t, utf8.ValidString(invalidResult[1].message.Content))
	require.LessOrEqual(t, estimateTextTokens(invalidResult[1].message.Content), compressionSingleToolTokens)
}

func TestProgressiveContextKeepsToolCallProtocolValid(t *testing.T) {
	store := NewMemoryStore(t.TempDir(), 128000)
	messages := []*schema.Message{
		schema.UserMessage(strings.Repeat("old request ", 200)),
		toolCallMessage("call-1", "read_file", `{"path":"one"}`),
		schema.ToolMessage(strings.Repeat("same output ", 1000), "call-1", schema.WithToolName("read_file")),
		toolCallMessage("call-2", "read_file", `{"path":"two"}`),
		schema.ToolMessage(strings.Repeat("same output ", 1000), "call-2", schema.WithToolName("read_file")),
		schema.UserMessage("recent request"),
		schema.AssistantMessage("recent answer", nil),
	}

	result, err := store.microcompactOldToolResults("main", testContextEntries(messages), 100)
	require.NoError(t, err)
	requireToolProtocolValid(t, contextMessages(result))
	require.Equal(t, "call-1", result[2].message.ToolCallID)
	require.Equal(t, "call-2", result[4].message.ToolCallID)
}

func TestContextCollapsePersistsAndRestoresWithoutChangingHistory(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 1400)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".agentland"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".agentland", "MEMORY.md"), []byte("keep project rule"), 0o600))
	for index := 0; index < 8; index++ {
		require.NoError(t, store.Append("main", schema.UserMessage(strings.Repeat("user constraint ", 16))))
		require.NoError(t, store.Append("main", schema.AssistantMessage(strings.Repeat("implementation state ", 16), nil)))
	}
	historyPath := filepath.Join(workspace, ".agentland", "conversations", "main", "history.jsonl")
	before, err := os.ReadFile(historyPath)
	require.NoError(t, err)

	firstModel := &fakeModel{generateResponses: []*schema.Message{
		schema.AssistantMessage("<context_collapse>requirements and files preserved</context_collapse>", nil),
	}}
	first, err := store.Context(context.Background(), "main", "system", firstModel)
	require.NoError(t, err)
	require.Equal(t, 1, firstModel.generateCalls)
	require.Contains(t, first[0].Content, "system")
	require.Contains(t, first[0].Content, "keep project rule")
	require.Contains(t, first[1].Content, "Earlier conversation context fold")
	require.Contains(t, first[len(first)-2].Content, "user constraint")
	require.Contains(t, first[len(first)-1].Content, "implementation state")
	_, err = os.Stat(filepath.Join(workspace, ".agentland", "conversations", "main", "collapse.json"))
	require.NoError(t, err)

	restarted := NewMemoryStore(workspace, 1400)
	secondModel := &fakeModel{}
	second, err := restarted.Context(context.Background(), "main", "system", secondModel)
	require.NoError(t, err)
	require.Zero(t, secondModel.generateCalls)
	require.Equal(t, first, second)
	after, err := os.ReadFile(historyPath)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestSnipOnlyRemovesOldReasoningFromContextView(t *testing.T) {
	messages := make([]*schema.Message, 0, 12)
	for index := 0; index < 6; index++ {
		messages = append(messages, schema.UserMessage(strings.Repeat("request ", 40)))
		assistant := schema.AssistantMessage(strings.Repeat("answer ", 40), nil)
		assistant.ReasoningContent = strings.Repeat("reasoning ", 100)
		messages = append(messages, assistant)
	}
	entries := testContextEntries(messages)
	result := snipOldReasoning(entries, 100)

	require.Empty(t, result[1].message.ReasoningContent)
	require.NotEmpty(t, result[len(result)-1].message.ReasoningContent)
	require.NotEmpty(t, entries[1].message.ReasoningContent)
	require.NotEmpty(t, entries[len(entries)-1].message.ReasoningContent)
}

func TestContextCollapseAndAutoCompactFailuresSuppressRetry(t *testing.T) {
	store := NewMemoryStore(t.TempDir(), 1400)
	for index := 0; index < 8; index++ {
		require.NoError(t, store.Append("main", schema.UserMessage(strings.Repeat("user constraint ", 16))))
		require.NoError(t, store.Append("main", schema.AssistantMessage(strings.Repeat("implementation state ", 16), nil)))
	}
	chatModel := &fakeModel{generateErr: errors.New("compressor unavailable")}

	first, err := store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.NotEmpty(t, first)
	require.Equal(t, 2, chatModel.generateCalls)
	second, err := store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.NotEmpty(t, second)
	require.Equal(t, 2, chatModel.generateCalls)
}

func TestContextCollapseFailureFallsBackToAutoCompact(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 1400)
	for index := 0; index < 8; index++ {
		require.NoError(t, store.Append("main", schema.UserMessage(strings.Repeat("user constraint ", 16))))
		require.NoError(t, store.Append("main", schema.AssistantMessage(strings.Repeat("implementation state ", 16), nil)))
	}
	chatModel := &fakeModel{generateResponses: []*schema.Message{
		schema.AssistantMessage("", nil),
		schema.AssistantMessage("draft state", nil),
		schema.AssistantMessage("<state_snapshot>verified state</state_snapshot>", nil),
	}}

	messages, err := store.Context(context.Background(), "main", "system", chatModel)
	require.NoError(t, err)
	require.Equal(t, 3, chatModel.generateCalls)
	require.Contains(t, messages[1].Content, "verified state")
	_, err = os.Stat(filepath.Join(workspace, ".agentland", "conversations", "main", "summary.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(workspace, ".agentland", "conversations", "main", "collapse.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestMemoryRemovesCollapseWithoutHistory(t *testing.T) {
	workspace := t.TempDir()
	store := NewMemoryStore(workspace, 128000)
	require.NoError(t, store.saveCollapse("main", &conversationCollapse{
		Version: 1, UpTo: 4, Offset: 100, Content: "<context_collapse>stale</context_collapse>", UpdatedAt: "now",
	}))

	messages, err := store.Context(context.Background(), "main", "system", &fakeModel{})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	_, err = os.Stat(filepath.Join(workspace, ".agentland", "conversations", "main", "collapse.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func testContextEntries(messages []*schema.Message) []contextMessage {
	entries := make([]contextMessage, len(messages))
	offset := int64(0)
	for index, message := range messages {
		offset += int64(messageSize(message) + 1)
		entries[index] = contextMessage{
			message: message, number: index + 1, nextOffset: offset,
			tokens: estimateMessageTokens(message), bytes: messageSize(message),
		}
	}
	return entries
}

func stageNames(reports []contextStageReport) []string {
	names := make([]string, len(reports))
	for index, report := range reports {
		names[index] = report.Name
	}
	return names
}

func requireToolProtocolValid(t *testing.T, messages []*schema.Message) {
	t.Helper()
	knownCalls := make(map[string]struct{})
	for _, message := range messages {
		if message.Role == schema.Assistant {
			for _, call := range message.ToolCalls {
				knownCalls[call.ID] = struct{}{}
			}
		}
		if message.Role == schema.Tool {
			_, ok := knownCalls[message.ToolCallID]
			require.Truef(t, ok, "tool result %s has no preceding call", message.ToolCallID)
		}
	}
}

func bytesOfLines(line []byte, count int) []byte {
	result := make([]byte, 0, (len(line)+1)*count)
	for range count {
		result = append(result, line...)
		result = append(result, '\n')
	}
	return result
}
