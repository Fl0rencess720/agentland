package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fl0rencess720/agentland/pkg/gateway/deployer"
	"github.com/Fl0rencess720/agentland/pkg/gateway/publisher"
	"github.com/gin-gonic/gin"
)

type imagePublisherStub struct {
	request publisher.Request
	err     error
}

type applicationDeployerStub struct {
	request deployer.Request
	err     error
}

func (s *applicationDeployerStub) Deploy(_ context.Context, request deployer.Request) (*deployer.Result, error) {
	s.request = request
	if s.err != nil {
		return nil, s.err
	}
	return &deployer.Result{URL: "https://app.example.com", Hostname: "app.example.com", DeploymentName: "app-123"}, nil
}

func (s *imagePublisherStub) Build(_ context.Context, request publisher.Request) (*publisher.Result, error) {
	s.request = request
	if s.err != nil {
		return nil, s.err
	}
	return &publisher.Result{ImageRef: "registry.example/apps/project_1:pub_1", Digest: "sha256:" + strings.Repeat("a", 64), Logs: "done"}, nil
}

func TestPublicationAcceptsAuthenticatedImmutableSnapshotAndBuilds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	publisherStub := &imagePublisherStub{}
	handler := &PublicationHandler{
		enabled: true, serviceToken: strings.Repeat("s", 32),
		publisher: publisherStub, deployer: &applicationDeployerStub{},
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications?project_id=project_1&release_id=pub_1&context=.&dockerfile=Dockerfile", strings.NewReader("snapshot"))
	ctx.Request.Header.Set("Content-Type", "application/gzip")
	ctx.Request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	handler.Publish(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	if string(publisherStub.request.Snapshot) != "snapshot" {
		t.Fatalf("build request did not contain fixed snapshot: %+v", publisherStub.request)
	}
	var result struct {
		publisher.Result
		DeploymentURL string `json:"deployment_url"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.ImageRef == "" || result.DeploymentURL == "" {
		t.Fatalf("invalid publication response: %v %s", err, recorder.Body.String())
	}
}

func TestPublicationRejectsOversizedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &PublicationHandler{
		enabled: true, serviceToken: strings.Repeat("s", 32),
		publisher: &imagePublisherStub{}, deployer: &applicationDeployerStub{},
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications?project_id=project_1&release_id=pub_1", strings.NewReader(strings.Repeat("x", int(maxWorkspaceSnapshotBytes+1))))
	ctx.Request.Header.Set("Content-Type", "application/gzip")
	ctx.Request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	handler.Publish(ctx)
	if recorder.Code != http.StatusRequestEntityTooLarge || !strings.Contains(recorder.Body.String(), "snapshot") {
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
	handler := &PublicationHandler{
		enabled: true, serviceToken: strings.Repeat("s", 32),
		publisher: &imagePublisherStub{err: &publisher.BuildError{Err: context.DeadlineExceeded, Logs: "step output"}}, deployer: &applicationDeployerStub{},
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications?project_id=project_1&release_id=pub_1", strings.NewReader("snapshot"))
	ctx.Request.Header.Set("Content-Type", "application/gzip")
	ctx.Request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	handler.Publish(ctx)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"logs":"step output"`) {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicationDeploysBuiltDigestAndReportsDeploymentFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deployerStub := &applicationDeployerStub{err: errors.New("rollout timed out")}
	handler := &PublicationHandler{
		enabled: true, serviceToken: strings.Repeat("s", 32),
		publisher: &imagePublisherStub{}, deployer: deployerStub,
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/publications?project_id=project_1&release_id=pub_1", strings.NewReader("snapshot"))
	ctx.Request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	handler.Publish(ctx)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "APPLICATION_DEPLOY_FAILED") {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	if deployerStub.request.ImageRef == "" || deployerStub.request.Digest == "" {
		t.Fatalf("built image was not passed to deployer: %+v", deployerStub.request)
	}
}
