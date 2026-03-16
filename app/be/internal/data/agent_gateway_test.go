package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAgentlandGatewayClientEnsureSessionReadyAndWorkspaceOps(t *testing.T) {
	client := &agentlandGatewayClient{
		baseURL: "http://agentland-gateway.local",
		httpClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.URL.Path == "/api/agent-sessions/invocations/health":
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`))}
					resp.Header.Set(agentlandSessionHeader, "session_123")
					resp.Header.Set("Content-Type", "application/json")
					return resp, nil
				case r.URL.Path == "/api/agent-sessions/session_123/endpoints/by-port/8000/v1/chat/stream":
					var req models.AgentChatStreamReq
					require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
					require.Equal(t, "Build a dashboard", req.Message)
					require.True(t, req.Deep)
					body := bytes.NewBuffer(nil)
					_, _ = fmt.Fprint(body, "event: route\n")
					_, _ = fmt.Fprint(body, "data: {\"intent\":\"task\"}\n\n")
					_, _ = fmt.Fprint(body, "event: session\n")
					_, _ = fmt.Fprint(body, "data: {\"session_id\":\"task_123\",\"workspace_path\":\"/workspace\"}\n\n")
					_, _ = fmt.Fprint(body, "event: done\n")
					_, _ = fmt.Fprint(body, "data: {\"status\":\"complete\"}\n\n")
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}
					resp.Header.Set("Content-Type", "text/event-stream")
					return resp, nil
				case r.URL.Path == "/api/previews":
					require.Equal(t, "session_123", r.Header.Get(agentlandSessionHeader))
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"msg":"ok","code":200,"data":{"session_id":"session_123","port":3000,"preview_token":"pv_123","preview_url":"http://gateway/p/pv_123/","expires_at":"2026-03-13T12:30:00Z"}}`))}
					resp.Header.Set("Content-Type", "application/json")
					return resp, nil
				case r.URL.Path == "/api/code-runner/fs/tree":
					require.Equal(t, "session_123", r.Header.Get(agentlandSessionHeader))
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"msg":"ok","code":200,"data":{"root":"/workspace","nodes":[{"path":"src","name":"src","type":"dir"},{"path":"src/main.tsx","name":"main.tsx","type":"file","size":12}]}}`))}
					resp.Header.Set("Content-Type", "application/json")
					return resp, nil
				case r.URL.Path == "/api/code-runner/fs/file":
					require.Equal(t, "session_123", r.Header.Get(agentlandSessionHeader))
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"msg":"ok","code":200,"data":{"path":"/workspace/src/main.tsx","size":12,"encoding":"utf8","content":"hello world"}}`))}
					resp.Header.Set("Content-Type", "application/json")
					return resp, nil
				case r.URL.Path == "/api/code-runner/contexts":
					require.Equal(t, "session_123", r.Header.Get(agentlandSessionHeader))
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"msg":"ok","code":200,"data":{"context_id":"ctx_123","language":"bash","cwd":"/workspace","state":"ready","created_at":"2026-03-13T12:00:00Z"}}`))}
					resp.Header.Set("Content-Type", "application/json")
					return resp, nil
				case r.URL.Path == "/api/code-runner/contexts/ctx_123/execute":
					require.Equal(t, "session_123", r.Header.Get(agentlandSessionHeader))
					body := bytes.NewBuffer(nil)
					_, _ = fmt.Fprint(body, "data: {\"type\":\"init\",\"context_id\":\"ctx_123\",\"execution_id\":\"exec_123\"}\n\n")
					_, _ = fmt.Fprint(body, "data: {\"type\":\"execution_complete\",\"context_id\":\"ctx_123\",\"execution_id\":\"exec_123\",\"exit_code\":0,\"execution_time\":10}\n\n")
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}
					resp.Header.Set("Content-Type", "text/event-stream")
					return resp, nil
				case r.URL.Path == "/api/agent-sessions/session_123/endpoints/by-port/3000/":
					resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}
					return resp, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
				}
			}),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := client.EnsureSessionReady(ctx)
	require.NoError(t, err)
	require.Equal(t, "session_123", session.GatewaySessionID)

	events := make([]string, 0)
	err = client.StreamChat(ctx, session.GatewaySessionID, &models.AgentChatStreamReq{Message: "Build a dashboard", Deep: true}, func(event *models.AgentSSEEvent) error {
		events = append(events, event.Event)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"route", "session", "done"}, events)

	previewInfo, err := client.CreatePreview(ctx, session.GatewaySessionID, 3000)
	require.NoError(t, err)
	require.Equal(t, "pv_123", previewInfo.PreviewToken)
	require.Equal(t, "/p/pv_123/", previewInfo.PreviewURL)

	tree, err := client.GetFSTree(ctx, session.GatewaySessionID, "/workspace", 3)
	require.NoError(t, err)
	require.Equal(t, "/workspace", tree.Root)
	require.Len(t, tree.Nodes, 2)

	file, err := client.GetFSFile(ctx, session.GatewaySessionID, "/workspace/src/main.tsx", "utf8")
	require.NoError(t, err)
	require.Equal(t, "hello world", file.Content)

	ctxInfo, err := client.CreateExecContext(ctx, session.GatewaySessionID, "bash", "/workspace")
	require.NoError(t, err)
	require.Equal(t, "ctx_123", ctxInfo.ContextID)

	execResult, err := client.ExecuteInContext(ctx, session.GatewaySessionID, ctxInfo.ContextID, "echo ok", 1000)
	require.NoError(t, err)
	require.Equal(t, int32(0), execResult.ExitCode)

	statusCode, err := client.ProbePort(ctx, session.GatewaySessionID, 3000, "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, statusCode)
}

func TestAgentlandGatewayClientStreamClientDisablesTimeout(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})

	client := &agentlandGatewayClient{
		baseURL: "http://agentland-gateway.local",
		httpClient: &http.Client{
			Timeout:   65 * time.Second,
			Transport: transport,
		},
	}

	streamClient := client.streamClient()
	require.Zero(t, streamClient.Timeout)
	require.NotNil(t, streamClient.Transport)
}

func TestNormalizePreviewURL(t *testing.T) {
	require.Equal(t, "/p/pv_123/", normalizePreviewURL("http://gateway/p/pv_123/"))
	require.Equal(t, "/p/pv_123/?a=1#frag", normalizePreviewURL("http://gateway/p/pv_123/?a=1#frag"))
	require.Equal(t, "/p/pv_123/", normalizePreviewURL("/p/pv_123/"))
	require.Equal(t, "http://gateway/other/path", normalizePreviewURL("http://gateway/other/path"))
	require.Equal(t, "", normalizePreviewURL("   "))
}
