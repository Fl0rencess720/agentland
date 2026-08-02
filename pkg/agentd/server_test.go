package agentd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestServerStreamsChatAndReturnsHistory(t *testing.T) {
	workspace := t.TempDir()
	server, err := newServer(context.Background(), &Config{
		Port:          "1883",
		WorkspaceRoot: workspace,
		ContextTokens: 128000,
		AuthEnabled:   false,
	}, &fakeModel{responses: []*schema.Message{schema.AssistantMessage("hello", nil)}})
	require.NoError(t, err)
	defer server.Close()

	request := httptest.NewRequest(http.MethodPost, "/api/chat", bytes.NewBufferString(`{"conversation_id":"main","message":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "text/event-stream", response.Header().Get("Content-Type"))
	require.Contains(t, response.Body.String(), "event: run.started")
	require.Contains(t, response.Body.String(), "event: message.delta")
	require.Contains(t, response.Body.String(), "event: run.completed")

	request = httptest.NewRequest(http.MethodGet, "/api/conversations/main/messages", nil)
	response = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"content":"hi"`)
	require.Contains(t, response.Body.String(), `"content":"hello"`)
}
