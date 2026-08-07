package data

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/stretchr/testify/require"
)

func TestGatewayUsesAgentInvocationContracts(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := func(status int, body string) *http.Response {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
		}
		switch r.URL.Path {
		case "/api/agent-sessions/invocations/health":
			require.Equal(t, "default-runtime", r.URL.Query().Get("runtime"))
			result := response(http.StatusOK, `{"status":"ok"}`)
			result.Header.Set(agentlandSessionHeader, "session-1")
			return result, nil
		case "/api/agent-sessions/invocations/api/chat":
			require.Equal(t, "session-1", r.Header.Get(agentlandSessionHeader))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.JSONEq(t, `{"conversation_id":"project-1","message":"build an app","capture_trajectory":true}`, string(body))
			result := response(http.StatusOK, "id: 1\nevent: run.started\ndata: {\"type\":\"run.started\",\"run_id\":\"agent-run-1\",\"conversation_id\":\"project-1\",\"sequence\":1,\"timestamp\":\"2026-08-02T00:00:00Z\",\"payload\":{}}\n\n")
			result.Header.Set(agentlandSessionHeader, "session-1")
			result.Header.Set("Content-Type", "text/event-stream")
			return result, nil
		case "/api/agent-sessions/invocations/api/workspace/tree":
			result := response(http.StatusOK, `{"root":"/workspace","nodes":[{"path":"main.go","name":"main.go","type":"file","size":12}]}`)
			result.Header.Set(agentlandSessionHeader, "session-1")
			return result, nil
		case "/api/agent-sessions/invocations/api/workspace/file":
			if r.Method == http.MethodGet {
				result := response(http.StatusOK, `{"path":"main.go","size":12,"content":"package main","sha":"sha-1"}`)
				result.Header.Set(agentlandSessionHeader, "session-1")
				return result, nil
			}
			require.Equal(t, http.MethodPost, r.Method)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.JSONEq(t, `{"content":"package main\n","sha":"sha-1"}`, string(body))
			result := response(http.StatusOK, `{"path":"main.go","size":13,"sha":"sha-2"}`)
			result.Header.Set(agentlandSessionHeader, "session-1")
			return result, nil
		case "/api/previews":
			return response(http.StatusOK, `{"msg":"ok","code":200,"data":{"session_id":"session-1","port":3000,"preview_token":"preview-1","preview_url":"http://gateway/p/preview-1/","expires_at":"2026-08-02T01:00:00Z"}}`), nil
		default:
			return response(http.StatusNotFound, "not found"), nil
		}
	})
	httpClient := &http.Client{Transport: transport}
	client := &agentlandGatewayClient{baseURL: "http://gateway", httpClient: httpClient, streamClient: httpClient, runtimeName: "default-runtime", runtimeNamespace: "agentland-sandboxes"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessionID, err := client.EnsureRuntime(ctx, "")
	require.NoError(t, err)
	require.Equal(t, "session-1", sessionID)
	events := make([]*models.AgentEvent, 0)
	require.NoError(t, client.StreamChat(ctx, sessionID, "project-1", "build an app", func(event *models.AgentEvent) error { events = append(events, event); return nil }))
	require.Len(t, events, 1)
	require.Equal(t, "agent-run-1", events[0].RunID)
	tree, err := client.GetFileTree(ctx, sessionID, ".")
	require.NoError(t, err)
	require.Len(t, tree.Nodes, 1)
	file, err := client.GetFile(ctx, sessionID, "main.go")
	require.NoError(t, err)
	require.Equal(t, "sha-1", file.SHA)
	written, err := client.PutFile(ctx, sessionID, "main.go", "package main\n", "sha-1")
	require.NoError(t, err)
	require.Equal(t, "sha-2", written.SHA)
	preview, err := client.CreatePreview(ctx, sessionID, 3000)
	require.NoError(t, err)
	require.Equal(t, "http://preview-1.localhost:18081/p/preview-1/", preview.PreviewURL)
}

func TestGatewaySnapshotAndReplayContracts(t *testing.T) {
	snapshot := []byte("compressed-workspace")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := func(status int, body []byte) *http.Response {
			result := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}
			result.Header.Set(agentlandSessionHeader, "session-1")
			return result
		}
		switch request.URL.Path {
		case "/api/agent-sessions/invocations/api/workspace/snapshot":
			if request.Method == http.MethodGet {
				return response(http.StatusOK, snapshot), nil
			}
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			require.Equal(t, snapshot, body)
			return response(http.StatusOK, []byte(`{"restored":true}`)), nil
		case "/api/agent-sessions/invocations/api/replays/decision", "/api/agent-sessions/invocations/api/replays/live":
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), `"records"`)
			mode := "decision"
			if strings.HasSuffix(request.URL.Path, "/live") {
				mode = "live"
			}
			return response(http.StatusOK, []byte(`{"mode":"`+mode+`","status":"completed","total_steps":2,"matched_steps":2,"score":1}`)), nil
		default:
			return response(http.StatusNotFound, []byte(`{"error":"not found"}`)), nil
		}
	})
	client := &agentlandGatewayClient{baseURL: "http://gateway", httpClient: &http.Client{Transport: transport}}

	got, err := client.GetWorkspaceSnapshot(context.Background(), "session-1")
	require.NoError(t, err)
	require.Equal(t, snapshot, got)
	require.NoError(t, client.RestoreWorkspaceSnapshot(context.Background(), "session-1", snapshot))
	records := []models.RunTrajectoryRecord{{Version: 1, RunID: "run", Sequence: 1, Hash: "hash"}}
	decision, err := client.ReplayDecisions(context.Background(), "session-1", records)
	require.NoError(t, err)
	require.Equal(t, "decision", decision.Mode)
	live, err := client.ReplayLive(context.Background(), "session-1", records)
	require.NoError(t, err)
	require.Equal(t, "live", live.Mode)
}

func TestGatewayPublicationUsesServiceAuthentication(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "/api/publications", request.URL.Path)
		require.Equal(t, "Bearer publisher-secret", request.Header.Get("Authorization"))
		require.Equal(t, "session-1", request.Header.Get(agentlandSessionHeader))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"project_id":"project-1","release_id":"pub-1","context":".","dockerfile":"Dockerfile"}`, string(body))
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Request: request,
			Body: io.NopCloser(strings.NewReader(`{"image_ref":"registry.example/apps/project-1:pub-1","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","logs":"done"}`)),
		}, nil
	})
	client := &agentlandGatewayClient{
		baseURL: "http://gateway", streamClient: &http.Client{Transport: transport}, publisherToken: "publisher-secret",
	}
	result, err := client.PublishImage(context.Background(), "session-1", "project-1", "pub-1", ".", "Dockerfile")
	require.NoError(t, err)
	require.Equal(t, "registry.example/apps/project-1:pub-1", result.ImageRef)
}

func TestGatewaySendsExplicitEmptySHAWhenRecreatingFile(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"content":"restored","sha":""}`, string(body))
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"path":"main.go","size":8,"sha":"sha-new"}`)), Request: request}
		response.Header.Set(agentlandSessionHeader, "session-1")
		return response, nil
	})
	client := &agentlandGatewayClient{baseURL: "http://gateway", httpClient: &http.Client{Transport: transport}}

	written, err := client.PutFile(context.Background(), "session-1", "main.go", "restored", "")
	require.NoError(t, err)
	require.Equal(t, "sha-new", written.SHA)
}

func TestGatewayRejectsReplacementRuntime(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`)), Request: r}
		response.Header.Set(agentlandSessionHeader, "new-session")
		return response, nil
	})
	httpClient := &http.Client{Transport: transport}
	client := &agentlandGatewayClient{baseURL: "http://gateway", httpClient: httpClient, streamClient: httpClient, runtimeName: "default-runtime"}
	_, err := client.EnsureRuntime(context.Background(), "expired-session")
	var gatewayErr *models.GatewayResponseError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, "PROJECT_RUNTIME_EXPIRED", gatewayErr.Code)
}

func TestGatewayChecksReplacementBeforeInvocationError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"FILE_CONFLICT"}`)), Request: r}
		response.Header.Set(agentlandSessionHeader, "new-session")
		return response, nil
	})
	httpClient := &http.Client{Transport: transport}
	client := &agentlandGatewayClient{baseURL: "http://gateway", httpClient: httpClient, streamClient: httpClient}
	_, err := client.PutFile(context.Background(), "expired-session", "main.go", "content", "old-sha")
	var gatewayErr *models.GatewayResponseError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, "PROJECT_RUNTIME_EXPIRED", gatewayErr.Code)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestGatewayRejectsUnsafePreviewToken(t *testing.T) {
	client := &agentlandGatewayClient{
		baseURL: "http://gateway",
		httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"msg":"ok","code":200,"data":{"session_id":"session-1","port":3000,"preview_token":"unsafe_token","preview_url":"http://gateway/p/unsafe_token/","expires_at":"2026-08-02T01:00:00Z"}}`)),
				Request:    request,
			}, nil
		})},
	}
	_, err := client.CreatePreview(context.Background(), "session-1", 3000)
	require.ErrorContains(t, err, "unsafe hostname characters")
}

func TestInvocationNotFoundDoesNotExpireRuntime(t *testing.T) {
	client := &agentlandGatewayClient{
		baseURL: "http://gateway",
		httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"path not found"}`)),
				Request:    r,
			}, nil
		})},
	}

	_, err := client.GetFile(context.Background(), "session-1", "missing.txt")
	var gatewayErr *models.GatewayResponseError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusNotFound, gatewayErr.StatusCode)
	require.NotEqual(t, "PROJECT_RUNTIME_EXPIRED", gatewayErr.Code)
}
