package agentd

import (
	"context"
	"os"
	"path/filepath"
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
	_, err = os.Stat(filepath.Join(workspace, ".agentland", "conversations", "main", "summary.json"))
	require.NoError(t, err)
}
