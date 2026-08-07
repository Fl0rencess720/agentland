package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/gateway/publisher"
	"github.com/gin-gonic/gin"
)

type imagePublisherStub struct {
	request publisher.Request
	err     error
}

func (s *imagePublisherStub) Build(_ context.Context, request publisher.Request) (*publisher.Result, error) {
	s.request = request
	if s.err != nil {
		return nil, s.err
	}
	return &publisher.Result{ImageRef: "registry.example/apps/project_1:pub_1", Digest: "sha256:" + strings.Repeat("a", 64), Logs: "done"}, nil
}

func TestPublicationFetchesAuthenticatedSnapshotAndBuilds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var authorization, session string
	sandbox := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization, session = request.Header.Get("Authorization"), request.Header.Get(SessionHeader)
		if request.URL.Path != "/api/workspace/snapshot" {
			t.Fatalf("unexpected snapshot path %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte("snapshot"))
	}))
	defer sandbox.Close()
	publisherStub := &imagePublisherStub{}
	handler := &PublicationHandler{
		enabled: true, serviceToken: strings.Repeat("s", 32),
		sessionStore: &mockSessionStore{getSessionFn: func(context.Context, string) (*db.SandboxInfo, error) {
			return &db.SandboxInfo{GrpcEndpoint: sandbox.URL}, nil
		}},
		tokenSigner: &mockTokenSigner{signFn: func(string, string, int64) (string, error) { return "sandbox-token", nil }},
		publisher:   publisherStub, httpClient: sandbox.Client(),
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications", strings.NewReader(`{"project_id":"project_1","release_id":"pub_1","context":".","dockerfile":"Dockerfile"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set(SessionHeader, "session-1")
	ctx.Request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	handler.Publish(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	if authorization != "Bearer sandbox-token" || session != "session-1" || string(publisherStub.request.Snapshot) != "snapshot" {
		t.Fatalf("snapshot authentication or build request mismatch: auth=%q session=%q request=%+v", authorization, session, publisherStub.request)
	}
	var result publisher.Result
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.ImageRef == "" {
		t.Fatalf("invalid publication response: %v %s", err, recorder.Body.String())
	}
}

func TestPublicationRejectsOversizedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sandbox := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", int(maxWorkspaceSnapshotBytes+1))))
	}))
	defer sandbox.Close()
	handler := &PublicationHandler{
		enabled: true, serviceToken: strings.Repeat("s", 32),
		sessionStore: &mockSessionStore{getSessionFn: func(context.Context, string) (*db.SandboxInfo, error) {
			return &db.SandboxInfo{GrpcEndpoint: sandbox.URL}, nil
		}},
		tokenSigner: &mockTokenSigner{signFn: func(string, string, int64) (string, error) { return "token", nil }},
		publisher:   &imagePublisherStub{}, httpClient: sandbox.Client(),
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications", strings.NewReader(`{"project_id":"project_1","release_id":"pub_1"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set(SessionHeader, "session-1")
	ctx.Request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	handler.Publish(ctx)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "snapshot exceeds") {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicationRequiresServiceAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &PublicationHandler{enabled: true, serviceToken: strings.Repeat("s", 32)}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications", strings.NewReader(`{}`))
	handler.Publish(ctx)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "UNAUTHORIZED") {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicationReturnsBuildLogsSeparatelyFromError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sandbox := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte("snapshot")) }))
	defer sandbox.Close()
	handler := &PublicationHandler{
		enabled: true, serviceToken: strings.Repeat("s", 32),
		sessionStore: &mockSessionStore{getSessionFn: func(context.Context, string) (*db.SandboxInfo, error) {
			return &db.SandboxInfo{GrpcEndpoint: sandbox.URL}, nil
		}},
		tokenSigner: &mockTokenSigner{signFn: func(string, string, int64) (string, error) { return "token", nil }},
		publisher:   &imagePublisherStub{err: &publisher.BuildError{Err: context.DeadlineExceeded, Logs: "step output"}}, httpClient: sandbox.Client(),
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications", strings.NewReader(`{"project_id":"project_1","release_id":"pub_1"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set(SessionHeader, "session-1")
	ctx.Request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	handler.Publish(ctx)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"logs":"step output"`) {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
}
