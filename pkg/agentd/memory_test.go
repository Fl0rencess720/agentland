package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	require.GreaterOrEqual(t, len(messages), 3)
	require.Contains(t, messages[1].Content, "Conversation summary")
	summaryPath := filepath.Join(workspace, ".agentland", "conversations", "main", "summary.json")
	_, err = os.Stat(summaryPath)
	require.NoError(t, err)
	summary, err := store.loadSummary("main")
	require.NoError(t, err)
	require.Positive(t, summary.UpTo)
	require.Positive(t, summary.Offset)
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
	require.Len(t, messages, 3)
	require.Equal(t, "recent", messages[2].Content)
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

func bytesOfLines(line []byte, count int) []byte {
	result := make([]byte, 0, (len(line)+1)*count)
	for range count {
		result = append(result, line...)
		result = append(result, '\n')
	}
	return result
}
