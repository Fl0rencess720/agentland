package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gin-gonic/gin"
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

func TestServerStartsAndReplaysAsynchronousRun(t *testing.T) {
	server, err := newServer(context.Background(), &Config{WorkspaceRoot: t.TempDir(), ContextTokens: 128000}, &fakeModel{
		responses: []*schema.Message{schema.AssistantMessage("hello", nil)},
	})
	require.NoError(t, err)
	defer server.Close()

	response := serveRequest(server, http.MethodPost, "/api/runs", `{"run_id":"run-1","conversation_id":"main","message":"hi"}`)
	require.Equal(t, http.StatusAccepted, response.Code)
	require.Contains(t, response.Body.String(), `"status":"running"`)
	require.Eventually(t, func() bool {
		state := serveRequest(server, http.MethodGet, "/api/runs/run-1", "")
		return state.Code == http.StatusOK && strings.Contains(state.Body.String(), `"status":"completed"`)
	}, time.Second, 10*time.Millisecond)

	response = serveRequest(server, http.MethodGet, "/api/runs/run-1/events?after=1", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), "event: message.delta")
	require.Contains(t, response.Body.String(), "event: run.completed")

	response = serveRequest(server, http.MethodPost, "/api/runs", `{"run_id":"run-1","conversation_id":"main","message":"hi"}`)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"status":"completed"`)
}

func TestServerRejectsOversizedChatMessageAndBody(t *testing.T) {
	server := newTestServer(t, t.TempDir())
	defer server.Close()

	messageBody := fmt.Sprintf(`{"conversation_id":"main","message":%q}`, strings.Repeat("x", maxChatMessageBytes+1))
	response := serveRequest(server, http.MethodPost, "/api/chat", messageBody)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.Contains(t, response.Body.String(), "message exceeds")

	requestBody := fmt.Sprintf(`{"conversation_id":"main","message":%q}`, strings.Repeat("x", maxChatRequestBodyBytes+1))
	response = serveRequest(server, http.MethodPost, "/api/chat", requestBody)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.Contains(t, response.Body.String(), "request body is too large")
}

func TestServerRejectsMultipleChatJSONValues(t *testing.T) {
	server := newTestServer(t, t.TempDir())
	defer server.Close()

	response := serveRequest(server, http.MethodPost, "/api/chat", `{"conversation_id":"main","message":"hi"}{}`)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "invalid request body")
}

func TestServerWorkspaceFileReadWriteAndConflict(t *testing.T) {
	workspace := t.TempDir()
	server := newTestServer(t, workspace)
	defer server.Close()

	response := serveRequest(server, http.MethodPost, "/api/workspace/file?path=src%2Fapp.txt", `{"content":"hello","sha":""}`)
	require.Equal(t, http.StatusOK, response.Code)
	var created workspaceFileResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	require.Len(t, created.SHA, 64)
	require.Equal(t, int64(5), created.Size)
	require.Equal(t, "src/app.txt", created.Path)

	response = serveRequest(server, http.MethodGet, "/api/workspace/file?path=src%2Fapp.txt", "")
	require.Equal(t, http.StatusOK, response.Code)
	var read workspaceFileResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &read))
	require.Equal(t, "hello", read.Content)
	require.Equal(t, created.SHA, read.SHA)
	require.Equal(t, "src/app.txt", read.Path)

	response = serveRequest(server, http.MethodPost, "/api/workspace/file?path=src%2Fapp.txt", `{"content":"stale","sha":"0000"}`)
	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), `"code":"FILE_CONFLICT"`)
	require.Contains(t, response.Body.String(), created.SHA)

	body := fmt.Sprintf(`{"content":"updated","sha":%q}`, created.SHA)
	response = serveRequest(server, http.MethodPost, "/api/workspace/file?path=src%2Fapp.txt", body)
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), fileSHA([]byte("updated")))
}

func TestServerWorkspaceConditionalWriteWaitsForShell(t *testing.T) {
	workspace := shellTestWorkspace(t)
	tools := &localTools{root: workspace}
	_, err := tools.writeFile(context.Background(), writeFileInput{Path: "app.txt", Content: "old"})
	require.NoError(t, err)
	handler := &workspaceHandler{tools: tools}
	router := gin.New()
	router.POST("/api/workspace/file", handler.writeFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type shellCompletion struct {
		result shellOutput
		err    error
	}
	shellDone := make(chan shellCompletion, 1)
	go func() {
		result, shellErr := tools.shell(ctx, shellInput{
			Command: "printf started > .write-race-started; while [ ! -f .write-race-release ]; do sleep 0.01; done; printf shell > app.txt",
		})
		shellDone <- shellCompletion{result: result, err: shellErr}
	}()
	releasePath := filepath.Join(workspace, ".write-race-release")
	defer os.WriteFile(releasePath, nil, 0o600)
	markerPath := filepath.Join(workspace, ".write-race-started")
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waitingForMarker := true
	for waitingForMarker {
		select {
		case completion := <-shellDone:
			require.NoError(t, completion.err)
			t.Fatalf("shell exited before creating race marker: status %d: %s", completion.result.ExitCode, completion.result.Output)
		case <-ticker.C:
			_, statErr := os.Stat(markerPath)
			waitingForMarker = statErr != nil
		case <-deadline.C:
			t.Fatal("shell did not create race marker")
		}
	}

	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		body := fmt.Sprintf(`{"content":"ui","sha":%q}`, fileSHA([]byte("old")))
		request := httptest.NewRequest(http.MethodPost, "/api/workspace/file?path=app.txt", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		responseDone <- response
	}()

	select {
	case response := <-responseDone:
		t.Fatalf("conditional write completed while shell was active: status %d", response.Code)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, os.WriteFile(releasePath, nil, 0o600))
	completion := <-shellDone
	require.NoError(t, completion.err)
	require.Zero(t, completion.result.ExitCode, completion.result.Output)

	select {
	case response := <-responseDone:
		require.Equal(t, http.StatusConflict, response.Code)
		require.Contains(t, response.Body.String(), `"code":"FILE_CONFLICT"`)
	case <-time.After(2 * time.Second):
		t.Fatal("conditional write remained blocked after shell completed")
	}
	content, err := os.ReadFile(filepath.Join(workspace, "app.txt"))
	require.NoError(t, err)
	require.Equal(t, "shell", string(content))
}

func TestServerWorkspaceRejectsPathEscapes(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, "escape")))
	server := newTestServer(t, workspace)
	defer server.Close()

	response := serveRequest(server, http.MethodGet, "/api/workspace/file?path="+url.QueryEscape("../secret.txt"), "")
	require.Equal(t, http.StatusForbidden, response.Code)
	response = serveRequest(server, http.MethodGet, "/api/workspace/file?path="+url.QueryEscape("escape/secret.txt"), "")
	require.Equal(t, http.StatusForbidden, response.Code)
	response = serveRequest(server, http.MethodPost, "/api/workspace/file?path="+url.QueryEscape("escape/new.txt"), `{"content":"bad"}`)
	require.Equal(t, http.StatusForbidden, response.Code)
	response = serveRequest(server, http.MethodGet, "/api/workspace/tree?path="+url.QueryEscape("escape"), "")
	require.Equal(t, http.StatusForbidden, response.Code)
}

func TestServerWorkspaceRejectsAgentlandStateAndAliases(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".agentland", "conversations"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".agentland", "conversations", "history.jsonl"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(".agentland/conversations/history.jsonl", filepath.Join(workspace, "history-link")))
	server := newTestServer(t, workspace)
	defer server.Close()

	response := serveRequest(server, http.MethodGet, "/api/workspace/file?path=.agentland%2Fconversations%2Fhistory.jsonl", "")
	require.Equal(t, http.StatusForbidden, response.Code)
	response = serveRequest(server, http.MethodGet, "/api/workspace/file?path=history-link", "")
	require.Equal(t, http.StatusForbidden, response.Code)
	response = serveRequest(server, http.MethodPost, "/api/workspace/file?path=.agentland%2Fmcp.json", `{"content":"{}"}`)
	require.Equal(t, http.StatusForbidden, response.Code)
	response = serveRequest(server, http.MethodGet, "/api/workspace/tree?path=.agentland", "")
	require.Equal(t, http.StatusForbidden, response.Code)
}

func TestServerWorkspaceTreeIsSortedAndHonorsDepth(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "dir", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "z.txt"), []byte("z"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "dir", "b.txt"), []byte("b"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "dir", "nested", "hidden.txt"), []byte("x"), 0o600))
	for _, ignored := range []string{".agentland", ".git", ".next", ".venv", "__pycache__", "node_modules"} {
		require.NoError(t, os.MkdirAll(filepath.Join(workspace, ignored), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(workspace, ignored, "ignored.txt"), []byte("x"), 0o600))
	}
	server := newTestServer(t, workspace)
	defer server.Close()

	response := serveRequest(server, http.MethodGet, "/api/workspace/tree?depth=2", "")
	require.Equal(t, http.StatusOK, response.Code)
	var tree struct {
		Root  string              `json:"root"`
		Nodes []workspaceTreeNode `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &tree))
	resolvedRoot, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	require.Equal(t, filepath.ToSlash(resolvedRoot), tree.Root)
	paths := make([]string, 0, len(tree.Nodes))
	for _, node := range tree.Nodes {
		paths = append(paths, node.Path)
	}
	require.Equal(t, []string{"a.txt", "dir", "dir/b.txt", "dir/nested", "z.txt"}, paths)
}

func TestServerWorkspaceRejectsOversizedFile(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "large.txt")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxWorkspaceFileBytes+1))
	require.NoError(t, file.Close())
	server := newTestServer(t, workspace)
	defer server.Close()

	response := serveRequest(server, http.MethodGet, "/api/workspace/file?path=large.txt", "")
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.Contains(t, response.Body.String(), "file exceeds")
	response = serveRequest(server, http.MethodPost, "/api/workspace/file?path=new.txt", fmt.Sprintf(`{"content":%q}`, strings.Repeat("x", maxWorkspaceFileBytes+1)))
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.Contains(t, response.Body.String(), "content exceeds")

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	defer root.Close()
	sha, err := currentFileSHAAt(root, "large.txt")
	require.NoError(t, err)
	require.Len(t, sha, 64)
}

func TestServerWorkspaceTreeStopsAtNodeLimit(t *testing.T) {
	workspace := t.TempDir()
	for i := 0; i <= maxWorkspaceTreeNodes; i++ {
		name := filepath.Join(workspace, fmt.Sprintf("file-%04d.txt", i))
		require.NoError(t, os.WriteFile(name, nil, 0o600))
	}
	server := newTestServer(t, workspace)
	defer server.Close()

	response := serveRequest(server, http.MethodGet, "/api/workspace/tree?depth=1", "")
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.Contains(t, response.Body.String(), "workspace tree exceeds")
}

func TestServerProxyForwardsRequestAndResponse(t *testing.T) {
	type capturedRequest struct {
		Method        string
		Path          string
		Query         string
		Body          string
		Authorization string
		Session       string
		Custom        string
	}
	captured := make(chan capturedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- capturedRequest{
			Method:        r.Method,
			Path:          r.URL.Path,
			Query:         r.URL.RawQuery,
			Body:          string(body),
			Authorization: r.Header.Get("Authorization"),
			Session:       r.Header.Get("X-Agentland-Session"),
			Custom:        r.Header.Get("X-Custom"),
		}
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "proxied")
	}))
	defer upstream.Close()
	port := serverPort(t, upstream.URL)

	server := newTestServer(t, t.TempDir())
	defer server.Close()
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/proxy/by-port/%d/v1/items?q=agent", port), strings.NewReader("payload"))
	request.Header.Set("Authorization", "Bearer internal")
	request.Header.Set("X-Agentland-Session", "session")
	request.Header.Set("X-Custom", "kept")
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code)
	require.Equal(t, "yes", response.Header().Get("X-Upstream"))
	require.Equal(t, "proxied", response.Body.String())
	got := <-captured
	require.Equal(t, http.MethodPost, got.Method)
	require.Equal(t, "/v1/items", got.Path)
	require.Equal(t, "q=agent", got.Query)
	require.Equal(t, "payload", got.Body)
	require.Empty(t, got.Authorization)
	require.Empty(t, got.Session)
	require.Equal(t, "kept", got.Custom)

	response = serveRequest(server, http.MethodGet, "/api/proxy/by-port/65536", "")
	require.Equal(t, http.StatusForbidden, response.Code)
}

func TestServerProxyStreamsResponse(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "second\n")
	}))
	defer upstream.Close()

	server := newTestServer(t, t.TempDir())
	defer server.Close()
	endpoint := httptest.NewServer(server.httpServer.Handler)
	defer endpoint.Close()
	response, err := http.Get(fmt.Sprintf("%s/api/proxy/by-port/%d/events", endpoint.URL, serverPort(t, upstream.URL)))
	require.NoError(t, err)
	defer response.Body.Close()
	first := make([]byte, len("first\n"))
	_, err = io.ReadFull(response.Body, first)
	require.NoError(t, err)
	require.Equal(t, "first\n", string(first))
	close(release)
	rest, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "second\n", string(rest))
}

func newTestServer(t *testing.T, workspace string) *Server {
	t.Helper()
	server, err := newServer(context.Background(), &Config{
		Port:          "1883",
		WorkspaceRoot: workspace,
		ContextTokens: 128000,
		AuthEnabled:   false,
	}, &fakeModel{responses: []*schema.Message{schema.AssistantMessage("hello", nil)}})
	require.NoError(t, err)
	return server
}

func serveRequest(server *Server, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}

func serverPort(t *testing.T, rawURL string) int {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	_, rawPort, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	return port
}
