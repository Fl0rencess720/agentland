package biz

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func testStringPointer(value string) *string { return &value }

type projectRepoStub struct{ project *models.Project }

func (r *projectRepoStub) CreateProject(_ context.Context, in *models.CreateProjectInput) (*models.Project, error) {
	r.project = &models.Project{ID: in.ID, OwnerID: in.OwnerID, Name: in.Name, Template: in.Template, Status: in.Status, CreatedAt: in.Now, UpdatedAt: in.Now}
	return r.project, nil
}
func (r *projectRepoStub) ListProjects(context.Context, *models.ProjectListFilter) ([]*models.Project, int, error) {
	return []*models.Project{r.project}, 1, nil
}
func (r *projectRepoStub) GetProjectByID(_ context.Context, ownerID, projectID string) (*models.Project, error) {
	if r.project == nil || r.project.OwnerID != ownerID || r.project.ID != projectID {
		return nil, autherr.ErrProjectNotFound
	}
	return r.project, nil
}
func (r *projectRepoStub) GetProjectAndTouch(ctx context.Context, ownerID, projectID string, now time.Time) (*models.Project, error) {
	p, err := r.GetProjectByID(ctx, ownerID, projectID)
	if p != nil {
		p.LastOpenedAt = &now
	}
	return p, err
}
func (r *projectRepoStub) UpdateProject(_ context.Context, in *models.UpdateProjectInput) (*models.Project, error) {
	r.project.Name, r.project.Metadata, r.project.UpdatedAt = in.Name, in.Metadata, in.Now
	return r.project, nil
}
func (r *projectRepoStub) SoftDeleteProject(context.Context, string, string, time.Time) error {
	return nil
}
func (r *projectRepoStub) CountActiveProjectsByOwner(context.Context, string) (int, error) {
	return 1, nil
}
func (r *projectRepoStub) GetUserPlan(context.Context, string) (string, error) { return "free", nil }

type runRepoStub struct {
	runtime          *models.ProjectRuntime
	run              *models.Run
	preview          *models.ProjectPreview
	created          int
	touched          int
	assistant        string
	finishedStatus   string
	finishedSequence int64
	cancelRequested  atomic.Bool
	heartbeatCalls   atomic.Int64
	upserts          atomic.Int64
	finished         atomic.Bool
	finishedCount    atomic.Int64
	eventsMu         sync.Mutex
	persistedEvents  []*models.AgentEvent
	heartbeat        func(context.Context, string, string, time.Time) (bool, error)
	claim            func(context.Context, string, string, time.Time) (*models.Run, error)
	appendDelta      func(string, int64)
}

func (r *runRepoStub) CreateRun(_ context.Context, in *models.CreateRunInput) (*models.Run, bool, error) {
	r.created++
	r.run = &models.Run{ID: in.ID, OwnerID: in.OwnerID, ProjectID: in.ProjectID, InputMessageID: in.InputMessageID, AssistantMessageID: in.AssistantMessageID, InputMessage: in.Message, Status: models.RunStatusQueued, TraceParent: in.TraceParent, TraceState: in.TraceState, CreatedAt: in.Now, UpdatedAt: in.Now}
	return r.run, false, nil
}
func (r *runRepoStub) FindRunByIdempotency(context.Context, string, string, string, string) (*models.Run, error) {
	return nil, nil
}
func (r *runRepoStub) GetRun(_ context.Context, ownerID, runID string) (*models.Run, error) {
	if r.run == nil || r.run.OwnerID != ownerID || r.run.ID != runID {
		return nil, ErrRunNotFound
	}
	return r.run, nil
}
func (r *runRepoStub) GetProjectRunState(context.Context, string, string) (*models.ProjectRunState, error) {
	return &models.ProjectRunState{}, nil
}
func (r *runRepoStub) ListMessages(context.Context, string, string, string, int) ([]*models.Message, *string, error) {
	return nil, nil, nil
}
func (r *runRepoStub) RequestCancel(context.Context, string, string, time.Time) (*models.Run, bool, error) {
	return r.run, false, nil
}
func (r *runRepoStub) GetRuntime(context.Context, string, string) (*models.ProjectRuntime, error) {
	return r.runtime, nil
}
func (r *runRepoStub) ExpireRuntime(_ context.Context, _, _ string, now time.Time) error {
	if r.runtime != nil {
		r.runtime.Status, r.runtime.UpdatedAt = models.RuntimeStatusExpired, now
	}
	return nil
}
func (r *runRepoStub) TouchRuntime(context.Context, string, time.Time) error { r.touched++; return nil }
func (r *runRepoStub) TouchRuntimeByPreviewToken(context.Context, string, time.Time) error {
	return nil
}
func (r *runRepoStub) SavePreview(_ context.Context, preview *models.ProjectPreview) error {
	r.preview = preview
	return nil
}
func (r *runRepoStub) GetPreview(context.Context, string, string) (*models.ProjectPreview, error) {
	if r.preview == nil {
		return nil, ErrPreviewNotFound
	}
	return r.preview, nil
}
func (r *runRepoStub) ClaimRun(ctx context.Context, runID, workerID string, now time.Time) (*models.Run, error) {
	if r.claim != nil {
		return r.claim(ctx, runID, workerID, now)
	}
	return nil, nil
}
func (r *runRepoStub) UpsertRuntime(_ context.Context, runtime *models.ProjectRuntime) error {
	r.upserts.Add(1)
	r.runtime = runtime
	return nil
}
func (r *runRepoStub) Heartbeat(ctx context.Context, runID, workerID string, now time.Time) (bool, error) {
	r.heartbeatCalls.Add(1)
	if r.heartbeat != nil {
		return r.heartbeat(ctx, runID, workerID, now)
	}
	return true, nil
}
func (r *runRepoStub) SetAgentRun(_ context.Context, _, _, agentRunID string, sequence int64, _ time.Time, event *models.AgentEvent) (bool, error) {
	r.run.AgentRunID, r.run.LastSequence = agentRunID, sequence
	if event != nil {
		r.eventsMu.Lock()
		r.persistedEvents = append(r.persistedEvents, event)
		r.eventsMu.Unlock()
	}
	return true, nil
}
func (r *runRepoStub) AppendAssistantDelta(_ context.Context, _, _, delta string, sequence int64, _ time.Time, events []*models.AgentEvent) (bool, error) {
	r.assistant += delta
	r.run.LastSequence = sequence
	if r.appendDelta != nil {
		r.appendDelta(delta, sequence)
	}
	r.eventsMu.Lock()
	r.persistedEvents = append(r.persistedEvents, events...)
	r.eventsMu.Unlock()
	return true, nil
}
func (r *runRepoStub) FinishRun(_ context.Context, _, _, status, _, _ string, sequence int64, _ time.Time, event *models.AgentEvent) (bool, error) {
	r.finishedStatus, r.finishedSequence = status, sequence
	r.finished.Store(true)
	r.finishedCount.Add(1)
	if event != nil {
		r.eventsMu.Lock()
		r.persistedEvents = append(r.persistedEvents, event)
		r.eventsMu.Unlock()
	}
	return true, nil
}
func (r *runRepoStub) FailOrphanedRuns(context.Context, time.Time, time.Time) ([]models.RunSequence, error) {
	return nil, nil
}
func (r *runRepoStub) IsCancelRequested(context.Context, string) (bool, error) {
	return r.cancelRequested.Load(), nil
}

type eventStoreStub struct {
	events  []*models.AgentEvent
	stored  []*models.StoredRunEvent
	after   string
	expired atomic.Int64
	publish func(context.Context, string, *models.AgentEvent) error
	expire  func(context.Context, string, time.Duration) error
}

func (s *eventStoreStub) Publish(ctx context.Context, runID string, event *models.AgentEvent) (string, error) {
	if s.publish != nil {
		if err := s.publish(ctx, runID, event); err != nil {
			return "", err
		}
	}
	s.events = append(s.events, event)
	return "1-0", nil
}
func (s *eventStoreStub) Read(_ context.Context, _, after string, _ time.Duration) ([]*models.StoredRunEvent, error) {
	s.after = after
	events := s.stored
	s.stored = nil
	return events, nil
}
func (s *eventStoreStub) Expire(ctx context.Context, runID string, ttl time.Duration) error {
	s.expired.Add(1)
	if s.expire != nil {
		return s.expire(ctx, runID, ttl)
	}
	return nil
}

type gatewayStub struct {
	file        *models.GatewayFile
	write       *models.GatewayFileWrite
	putErr      error
	getErr      error
	events      []*models.AgentEvent
	ensure      func(context.Context, string) (string, error)
	stream      func(context.Context, string, string, string, func(*models.AgentEvent) error) error
	streamCalls int
	putSHA      string
}

func (g *gatewayStub) EnsureRuntime(ctx context.Context, sessionID string) (string, error) {
	if g.ensure != nil {
		return g.ensure(ctx, sessionID)
	}
	return "session-1", nil
}
func (g *gatewayStub) StreamChat(ctx context.Context, sessionID, conversationID, message string, callback func(*models.AgentEvent) error) error {
	g.streamCalls++
	if g.stream != nil {
		return g.stream(ctx, sessionID, conversationID, message, callback)
	}
	for _, event := range g.events {
		if err := callback(event); err != nil {
			return err
		}
	}
	return nil
}
func (g *gatewayStub) CancelRun(context.Context, string, string) error { return nil }
func (g *gatewayStub) GetFileTree(context.Context, string, string) (*models.GatewayFileTree, error) {
	return &models.GatewayFileTree{Root: "."}, nil
}
func (g *gatewayStub) GetFile(context.Context, string, string) (*models.GatewayFile, error) {
	return g.file, g.getErr
}
func (g *gatewayStub) PutFile(_ context.Context, _, _, _, sha string) (*models.GatewayFileWrite, error) {
	g.putSHA = sha
	return g.write, g.putErr
}
func (g *gatewayStub) CreatePreview(context.Context, string, int) (*models.GatewayPreviewInfo, error) {
	return &models.GatewayPreviewInfo{PreviewToken: "preview-token", PreviewURL: "http://preview-token.localhost:18081/p/preview-token/", Port: 3000, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

type artifactRunRepoStub struct {
	*runRepoStub
	snapshot *models.WorkspaceSnapshot
	records  []models.RunTrajectoryRecord
}

func (r *artifactRunRepoStub) SaveWorkspaceSnapshot(_ context.Context, _, _ string, data []byte, sha, captureError string, now time.Time) (bool, error) {
	r.snapshot = &models.WorkspaceSnapshot{Data: append([]byte(nil), data...), SHA: sha, Error: captureError, CreatedAt: now}
	return true, nil
}

func (r *artifactRunRepoStub) AppendTrajectoryRecord(_ context.Context, _, _, _ string, _ int64, raw json.RawMessage, _ time.Time) (bool, error) {
	var record models.RunTrajectoryRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return false, err
	}
	r.records = append(r.records, record)
	return true, nil
}

func (r *artifactRunRepoStub) LoadWorkspaceSnapshot(context.Context, string) (*models.WorkspaceSnapshot, error) {
	return r.snapshot, nil
}

func (r *artifactRunRepoStub) LoadTrajectoryRecords(context.Context, string) ([]models.RunTrajectoryRecord, error) {
	return append([]models.RunTrajectoryRecord(nil), r.records...), nil
}

type replayGatewayStub struct {
	*gatewayStub
	snapshot []byte
	restored []byte
	decision *models.ReplayRunResp
	live     *models.ReplayRunResp
}

func (g *replayGatewayStub) GetWorkspaceSnapshot(context.Context, string) ([]byte, error) {
	return append([]byte(nil), g.snapshot...), nil
}

func (g *replayGatewayStub) RestoreWorkspaceSnapshot(_ context.Context, _ string, snapshot []byte) error {
	g.restored = append([]byte(nil), snapshot...)
	return nil
}

func (g *replayGatewayStub) ReplayDecisions(context.Context, string, []models.RunTrajectoryRecord) (*models.ReplayRunResp, error) {
	copy := *g.decision
	return &copy, nil
}

func (g *replayGatewayStub) ReplayLive(context.Context, string, []models.RunTrajectoryRecord) (*models.ReplayRunResp, error) {
	copy := *g.live
	return &copy, nil
}

func TestRunTrajectoryAndDecisionReplay(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.RunStatusCompleted, CreatedAt: now, UpdatedAt: now}
	records := []models.RunTrajectoryRecord{{Version: 1, RunID: "agent-run-1", ConversationID: "project-1", Sequence: 1, Type: "run.started", Hash: "hash-1"}}
	runs := &artifactRunRepoStub{runRepoStub: &runRepoStub{run: run}, records: records}
	gateway := &replayGatewayStub{
		gatewayStub: &gatewayStub{ensure: func(_ context.Context, session string) (string, error) {
			require.Empty(t, session)
			return "fresh-session", nil
		}},
		decision: &models.ReplayRunResp{Status: "completed", TotalSteps: 2, MatchedSteps: 2, Score: 1},
	}
	usecase := NewProjectUsecase(&projectRepoStub{}, runs, &eventStoreStub{}, gateway).(*projectUseCase)

	trajectory, apiErr := usecase.RunTrajectory(context.Background(), models.AuthPrincipal{UserID: "user-1"}, run.ID)
	require.Nil(t, apiErr)
	require.Equal(t, records, trajectory.Records)
	report, apiErr := usecase.ReplayRun(context.Background(), models.AuthPrincipal{UserID: "user-1"}, run.ID, &models.ReplayRunReq{Mode: models.ReplayModeDecision})
	require.Nil(t, apiErr)
	require.NotEmpty(t, report.ID)
	require.Equal(t, run.ID, report.SourceRunID)
	require.Equal(t, models.ReplayModeDecision, report.Mode)
	require.Equal(t, 1.0, report.Score)
}

func TestLiveReplayRestoresSnapshotInFreshRuntime(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.RunStatusCompleted, CreatedAt: now, UpdatedAt: now}
	source := []byte("source-snapshot")
	output := []byte("output-snapshot")
	sourceDigest := sha256.Sum256(source)
	runs := &artifactRunRepoStub{
		runRepoStub: &runRepoStub{run: run},
		snapshot:    &models.WorkspaceSnapshot{Data: source, SHA: fmt.Sprintf("%x", sourceDigest[:])},
		records:     []models.RunTrajectoryRecord{{Version: 1, RunID: "agent-run-1", Sequence: 1, Hash: "hash-1"}},
	}
	gateway := &replayGatewayStub{
		gatewayStub: &gatewayStub{}, snapshot: output,
		live: &models.ReplayRunResp{Status: "completed", TotalSteps: 3, MatchedSteps: 2, Score: 2.0 / 3.0},
	}
	usecase := NewProjectUsecase(&projectRepoStub{}, runs, &eventStoreStub{}, gateway).(*projectUseCase)
	report, apiErr := usecase.ReplayRun(context.Background(), models.AuthPrincipal{UserID: "user-1"}, run.ID, &models.ReplayRunReq{Mode: models.ReplayModeLive})
	require.Nil(t, apiErr)
	require.Equal(t, source, gateway.restored)
	require.True(t, report.WorkspaceChanged)
	require.Equal(t, runs.snapshot.SHA, report.SourceSnapshotSHA)
	require.NotEmpty(t, report.OutputSnapshotSHA)
}

func TestCreateRunReturnsAcceptedContract(t *testing.T) {
	previousTracer := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	tracerProvider := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		otel.SetTracerProvider(previousTracer)
		otel.SetTextMapPropagator(previousPropagator)
	})
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	usecase.now = func() time.Time { return now }
	ctx, span := tracerProvider.Tracer("test").Start(context.Background(), "request")
	defer span.End()
	result, apiErr := usecase.CreateRun(ctx, models.AuthPrincipal{UserID: "user-1"}, "project-1", "request-1", &models.RunCreateReq{Message: "build an app"})
	require.Nil(t, apiErr)
	require.NotEmpty(t, result.RunID)
	require.NotEmpty(t, result.UserMessageID)
	require.Equal(t, models.RunStatusQueued, result.Status)
	require.NotEmpty(t, runs.run.TraceParent)
}

func TestCreateRunRejectsExpiredRuntime(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{ProjectID: "project-1", OwnerID: "user-1", Status: models.RuntimeStatusActive, LastActiveAt: now.Add(-16 * time.Minute), ExpiresAt: now.Add(time.Hour)}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	usecase.now = func() time.Time { return now }
	_, apiErr := usecase.CreateRun(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", "request-1", &models.RunCreateReq{Message: "continue"})
	require.Equal(t, "PROJECT_RUNTIME_EXPIRED", apiErr.Data.Type)
	require.Zero(t, runs.created)
}

func TestCreateRunRejectsOversizedMessage(t *testing.T) {
	runs := &runRepoStub{}
	usecase := NewProjectUsecase(&projectRepoStub{}, runs, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	_, apiErr := usecase.CreateRun(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", "request-1", &models.RunCreateReq{Message: strings.Repeat("x", models.MaxRunMessageBytes+1)})
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Zero(t, runs.created)
}

func TestCreateRunRejectsRuntimePastAbsoluteExpiry(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{
		ProjectID: "project-1", OwnerID: "user-1", Status: models.RuntimeStatusActive,
		CreatedAt: now.Add(-time.Hour), LastActiveAt: now, ExpiresAt: now,
	}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	usecase.now = func() time.Time { return now }

	_, apiErr := usecase.CreateRun(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", "request-1", &models.RunCreateReq{Message: "continue"})
	require.Equal(t, "PROJECT_RUNTIME_EXPIRED", apiErr.Data.Type)
	require.Zero(t, runs.created)
	require.Equal(t, models.RuntimeStatusExpired, runs.runtime.Status)
}

func TestFileUpdateUsesGatewayAndTouchesRuntime(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	gateway := &gatewayStub{write: &models.GatewayFileWrite{Path: "main.go", Size: 12, SHA: "sha-2"}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, gateway).(*projectUseCase)
	usecase.now = func() time.Time { return now }
	result, apiErr := usecase.UpdateFileContent(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", &models.FileContentReq{Path: "main.go"}, &models.FileContentUpdateReq{Content: "package main", SHA: testStringPointer("sha-1")})
	require.Nil(t, apiErr)
	require.Equal(t, "sha-2", result.SHA)
	require.Equal(t, 1, runs.touched)
}

func TestFileUpdateAllowsExplicitEmptySHAForRecreation(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	gateway := &gatewayStub{write: &models.GatewayFileWrite{Path: "main.go", Size: 8, SHA: "sha-new"}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, gateway).(*projectUseCase)
	usecase.now = func() time.Time { return now }

	result, apiErr := usecase.UpdateFileContent(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", &models.FileContentReq{Path: "main.go"}, &models.FileContentUpdateReq{Content: "restored", SHA: testStringPointer("")})
	require.Nil(t, apiErr)
	require.Equal(t, "sha-new", result.SHA)
	require.Empty(t, gateway.putSHA)
}

func TestFileUpdateRejectsOversizedContent(t *testing.T) {
	usecase := NewProjectUsecase(&projectRepoStub{}, &runRepoStub{}, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	_, apiErr := usecase.UpdateFileContent(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", &models.FileContentReq{Path: "main.go"}, &models.FileContentUpdateReq{Content: strings.Repeat("x", models.MaxFileContentBytes+1), SHA: testStringPointer("sha")})
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
}

func TestFileUpdateMapsSHAConflict(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	gateway := &gatewayStub{putErr: &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "FILE_CONFLICT", SHA: "latest-sha"}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, gateway).(*projectUseCase)
	usecase.now = func() time.Time { return now }
	_, apiErr := usecase.UpdateFileContent(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", &models.FileContentReq{Path: "main.go"}, &models.FileContentUpdateReq{Content: "changed", SHA: testStringPointer("old-sha")})
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusConflict, apiErr.StatusCode)
	require.Equal(t, "FILE_CONFLICT", apiErr.Data.Type)
	require.Equal(t, "latest-sha", apiErr.Data.SHA)
}

func TestMissingWorkspaceFileDoesNotExpireRuntime(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	gateway := &gatewayStub{getErr: &models.GatewayResponseError{StatusCode: http.StatusNotFound, Message: "path not found"}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, gateway).(*projectUseCase)
	usecase.now = func() time.Time { return now }

	_, apiErr := usecase.FileContent(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", &models.FileContentReq{Path: "missing.txt"})
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusNotFound, apiErr.StatusCode)
	require.Equal(t, models.RuntimeStatusActive, runs.runtime.Status)
}

func TestPreviewWithoutRuntimePreviewIsIdle(t *testing.T) {
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	usecase := NewProjectUsecase(projects, &runRepoStub{}, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	result, apiErr := usecase.Preview(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1")
	require.Nil(t, apiErr)
	require.Equal(t, "idle", result.Status)
}

func TestStartPreviewSavesAndReturnsIsolatedURL(t *testing.T) {
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{
		ProjectID:        "project-1",
		OwnerID:          "user-1",
		GatewaySessionID: "session-1",
		Status:           models.RuntimeStatusActive,
		LastActiveAt:     now,
		ExpiresAt:        now.Add(time.Hour),
	}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	usecase.now = func() time.Time { return now }

	result, apiErr := usecase.StartPreview(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1", &models.PreviewStartReq{Port: 3000})
	require.Nil(t, apiErr)
	require.Equal(t, "http://preview-token.localhost:18081/p/preview-token/", result.PreviewURL)
	require.Equal(t, result.PreviewURL, runs.preview.PreviewURL)
}

func TestPreviewIsExpiredWhenRuntimeIsGone(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project-1", OwnerID: "user-1"}}
	runs := &runRepoStub{preview: &models.ProjectPreview{ID: "preview-1", ProjectID: "project-1", OwnerID: "user-1", Status: "running", PreviewURL: "http://token.localhost:18081/p/token/", ExpiresAt: now.Add(time.Hour)}}
	usecase := NewProjectUsecase(projects, runs, &eventStoreStub{}, &gatewayStub{}).(*projectUseCase)
	usecase.now = func() time.Time { return now }
	result, apiErr := usecase.Preview(context.Background(), models.AuthPrincipal{UserID: "user-1"}, "project-1")
	require.Nil(t, apiErr)
	require.Equal(t, "expired", result.Status)
}

func TestRunEventStreamResumesFromLastEventID(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.RunStatusRunning, CreatedAt: now, UpdatedAt: now}
	runs := &runRepoStub{run: run}
	event := &models.AgentEvent{Type: "run.completed", RunID: run.ID, Sequence: 4, Timestamp: now, Payload: json.RawMessage(`{}`)}
	data, err := json.Marshal(event)
	require.NoError(t, err)
	store := &eventStoreStub{stored: []*models.StoredRunEvent{{ID: "124-0", Type: "run.completed", Data: data}}}
	usecase := NewProjectUsecase(&projectRepoStub{}, runs, store, &gatewayStub{}).(*projectUseCase)
	var received []*models.StoredRunEvent
	apiErr := usecase.StreamRunEvents(context.Background(), models.AuthPrincipal{UserID: "user-1"}, run.ID, "123-0", func(item *models.StoredRunEvent) error {
		received = append(received, item)
		return nil
	})
	require.Nil(t, apiErr)
	require.Equal(t, "123-0", store.after)
	require.Len(t, received, 1)
	require.Equal(t, "124-0", received[0].ID)
}

func TestRunWorkerStreamsThroughGatewayAndPersistsTerminal(t *testing.T) {
	now := time.Now().UTC()
	previousTracer := otel.GetTracerProvider()
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		otel.SetTracerProvider(previousTracer)
	})
	requestCtx, requestSpan := tracerProvider.Tracer("test").Start(context.Background(), "run.create")
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(requestCtx, carrier)
	requestSpanID := requestSpan.SpanContext().SpanID()
	requestSpan.End()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, TraceParent: carrier.Get("traceparent"), CreatedAt: now}
	runs := &runRepoStub{run: run, runtime: &models.ProjectRuntime{ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", AgentConversationID: "project-1", Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour), UpdatedAt: now}}
	delta, _ := json.Marshal(map[string]string{"content": "hello"})
	gateway := &gatewayStub{events: []*models.AgentEvent{
		{Type: "run.started", RunID: "agent-run-1", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)},
		{Type: "message.delta", RunID: "agent-run-1", Sequence: 2, Timestamp: now, Payload: delta},
		{Type: "run.completed", RunID: "agent-run-1", Sequence: 3, Timestamp: now, Payload: json.RawMessage(`{}`)},
	}}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = time.Hour
	worker.execute(context.Background(), run)
	require.Equal(t, "hello", runs.assistant)
	require.Equal(t, models.RunStatusCompleted, runs.finishedStatus)
	require.Equal(t, int64(3), runs.finishedSequence)
	require.Len(t, runs.persistedEvents, 3)
	require.Equal(t, "run.completed", runs.persistedEvents[2].Type)
	require.Len(t, spanRecorder.Ended(), 2)
	runSpan := spanRecorder.Ended()[1]
	require.Equal(t, "run.execute", runSpan.Name())
	require.Equal(t, requestSpanID, runSpan.Parent().SpanID())
	require.Equal(t, codes.Ok, runSpan.Status().Code)
}

func TestRunWorkerPersistsPrivateTrajectoryAndWorkspaceSnapshot(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	baseRepo := &runRepoStub{run: run, runtime: &models.ProjectRuntime{
		ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", AgentConversationID: "project-1",
		Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour), UpdatedAt: now,
	}}
	runs := &artifactRunRepoStub{runRepoStub: baseRepo}
	first := signedTrajectoryRecord(t, models.RunTrajectoryRecord{
		Version: 1, RunID: "agent-run-1", ConversationID: "project-1", Sequence: 1,
		Type: "run.started", Timestamp: now.Format(time.RFC3339Nano), Payload: json.RawMessage(`{"message":"build"}`),
	})
	second := signedTrajectoryRecord(t, models.RunTrajectoryRecord{
		Version: 1, RunID: "agent-run-1", ConversationID: "project-1", Sequence: 2,
		Type: "run.finished", Timestamp: now.Add(time.Second).Format(time.RFC3339Nano), PreviousHash: first.Hash, Payload: json.RawMessage(`{"status":"completed"}`),
	})
	privateFirst, err := json.Marshal(first)
	require.NoError(t, err)
	privateSecond, err := json.Marshal(second)
	require.NoError(t, err)
	gateway := &replayGatewayStub{gatewayStub: &gatewayStub{events: []*models.AgentEvent{
		{Type: "trajectory.record", RunID: "agent-run-1", ConversationID: "project-1", Sequence: 1, Timestamp: now, Payload: privateFirst},
		{Type: "run.started", RunID: "agent-run-1", ConversationID: "project-1", Sequence: 2, Timestamp: now, Payload: json.RawMessage(`{}`)},
		{Type: "trajectory.record", RunID: "agent-run-1", ConversationID: "project-1", Sequence: 3, Timestamp: now, Payload: privateSecond},
		{Type: "run.completed", RunID: "agent-run-1", ConversationID: "project-1", Sequence: 4, Timestamp: now, Payload: json.RawMessage(`{}`)},
	}}, snapshot: []byte("workspace-before-run")}
	worker := NewRunWorker(runs, gateway)
	worker.workerID, worker.heartbeat = "worker-1", time.Hour
	worker.execute(context.Background(), run)

	require.Len(t, runs.records, 2)
	require.Equal(t, first.Hash, runs.records[0].Hash)
	require.Equal(t, []byte("workspace-before-run"), runs.snapshot.Data)
	require.Len(t, runs.persistedEvents, 2)
	require.Equal(t, "run.started", runs.persistedEvents[0].Type)
	require.Equal(t, "run.completed", runs.persistedEvents[1].Type)
}

func signedTrajectoryRecord(t *testing.T, record models.RunTrajectoryRecord) models.RunTrajectoryRecord {
	t.Helper()
	record.Hash = ""
	data, err := json.Marshal(record)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	record.Hash = fmt.Sprintf("%x", digest[:])
	return record
}

func TestRunWorkerCreatesRuntimeWithAbsoluteExpiry(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run}
	gateway := &gatewayStub{events: []*models.AgentEvent{
		{Type: "run.started", RunID: "agent-run-1", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)},
		{Type: "run.completed", RunID: "agent-run-1", Sequence: 2, Timestamp: now, Payload: json.RawMessage(`{}`)},
	}}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = time.Hour
	worker.runtimeMax = 45 * time.Minute
	worker.now = func() time.Time { return now }

	worker.execute(context.Background(), run)

	require.Equal(t, now.Add(45*time.Minute), runs.runtime.ExpiresAt)
}

func TestRunWorkerDoesNotExtendRuntimeAbsoluteExpiry(t *testing.T) {
	now := time.Now().UTC()
	expiresAt := now.Add(20 * time.Minute)
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run, runtime: &models.ProjectRuntime{
		ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", AgentConversationID: "project-1",
		Status: models.RuntimeStatusActive, CreatedAt: now.Add(-40 * time.Minute), LastActiveAt: now.Add(-time.Minute), ExpiresAt: expiresAt,
	}}
	gateway := &gatewayStub{events: []*models.AgentEvent{
		{Type: "run.started", RunID: "agent-run-1", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)},
		{Type: "run.completed", RunID: "agent-run-1", Sequence: 2, Timestamp: now, Payload: json.RawMessage(`{}`)},
	}}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = time.Hour
	worker.runtimeMax = time.Hour
	worker.now = func() time.Time { return now }

	worker.execute(context.Background(), run)

	require.Equal(t, expiresAt, runs.runtime.ExpiresAt)
	require.Equal(t, now, runs.runtime.LastActiveAt)
}

func TestRunWorkerPersistsTerminalWithTheRunState(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run, runtime: &models.ProjectRuntime{
		ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", AgentConversationID: "project-1",
		Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	gateway := &gatewayStub{events: []*models.AgentEvent{
		{Type: "run.started", RunID: "agent-run-1", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)},
		{Type: "run.completed", RunID: "agent-run-1", Sequence: 2, Timestamp: now, Payload: json.RawMessage(`{}`)},
	}}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = time.Millisecond
	worker.cancelPoll = time.Hour

	worker.execute(context.Background(), run)

	require.Len(t, runs.persistedEvents, 2)
	require.Equal(t, "run.completed", runs.persistedEvents[1].Type)
}

func TestRunWorkerFlushesAssistantDeltaOnInterval(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run, runtime: &models.ProjectRuntime{
		ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", AgentConversationID: "project-1",
		Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	flushed := make(chan struct{})
	var flushedOnce sync.Once
	runs.appendDelta = func(string, int64) { flushedOnce.Do(func() { close(flushed) }) }
	delta, _ := json.Marshal(map[string]string{"content": "hello"})
	gateway := &gatewayStub{stream: func(_ context.Context, _, _, _ string, callback func(*models.AgentEvent) error) error {
		if err := callback(&models.AgentEvent{Type: "run.started", RunID: "agent-run-1", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		if err := callback(&models.AgentEvent{Type: "message.delta", RunID: "agent-run-1", Sequence: 2, Timestamp: now, Payload: delta}); err != nil {
			return err
		}
		select {
		case <-flushed:
		case <-time.After(2 * time.Second):
			return errors.New("assistant delta was not flushed on schedule")
		}
		return callback(&models.AgentEvent{Type: "run.completed", RunID: "agent-run-1", Sequence: 3, Timestamp: now, Payload: json.RawMessage(`{}`)})
	}}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = time.Hour
	worker.cancelPoll = time.Hour

	worker.execute(context.Background(), run)

	require.Equal(t, "hello", runs.assistant)
	require.Equal(t, models.RunStatusCompleted, runs.finishedStatus)
	require.Len(t, runs.persistedEvents, 3)
	require.Equal(t, "message.delta", runs.persistedEvents[1].Type)
}

func TestRunWorkerDoesNotStartAgentAfterCancellationDuringRuntimePreparation(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run}
	gateway := &gatewayStub{}
	gateway.ensure = func(context.Context, string) (string, error) {
		runs.cancelRequested.Store(true)
		return "session-1", nil
	}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"

	worker.execute(context.Background(), run)

	require.Zero(t, gateway.streamCalls)
	require.Equal(t, models.RunStatusCancelled, runs.finishedStatus)
	require.Len(t, runs.persistedEvents, 1)
	require.Equal(t, "run.cancelled", runs.persistedEvents[0].Type)
}

func TestRunWorkerHeartbeatsWhilePreparingRuntime(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run}
	started := make(chan struct{})
	release := make(chan struct{})
	gateway := &gatewayStub{
		ensure: func(ctx context.Context, _ string) (string, error) {
			close(started)
			select {
			case <-release:
				return "session-1", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		events: []*models.AgentEvent{
			{Type: "run.started", RunID: "agent-run-1", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)},
			{Type: "run.completed", RunID: "agent-run-1", Sequence: 2, Timestamp: now, Payload: json.RawMessage(`{}`)},
		},
	}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = 5 * time.Millisecond
	worker.cancelPoll = time.Hour
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.execute(context.Background(), run)
	}()

	<-started
	require.Eventually(t, func() bool { return runs.heartbeatCalls.Load() >= 2 }, time.Second, 5*time.Millisecond)
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish after runtime became ready")
	}
	require.Equal(t, int64(1), runs.upserts.Load())
	require.Equal(t, 1, gateway.streamCalls)
}

func TestRunWorkerCancelsRuntimePreparationWhenLeaseIsLost(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run}
	runs.heartbeat = func(context.Context, string, string, time.Time) (bool, error) {
		return runs.heartbeatCalls.Load() < 2, nil
	}
	started := make(chan struct{})
	gateway := &gatewayStub{ensure: func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = 5 * time.Millisecond
	worker.cancelPoll = time.Hour
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.execute(context.Background(), run)
	}()

	<-started
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop runtime preparation after losing its lease")
	}
	require.GreaterOrEqual(t, runs.heartbeatCalls.Load(), int64(2))
	require.Zero(t, runs.upserts.Load())
	require.Zero(t, gateway.streamCalls)
}

func TestRunWorkerContinuesDuringTemporaryRedisLeaseFailure(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run, runtime: &models.ProjectRuntime{
		ProjectID: "project-1", OwnerID: "user-1", GatewaySessionID: "session-1", AgentConversationID: "project-1",
		Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	runs.heartbeat = func(context.Context, string, string, time.Time) (bool, error) {
		return false, errors.New("redis unavailable")
	}
	gateway := &gatewayStub{stream: func(ctx context.Context, _, _, _ string, callback func(*models.AgentEvent) error) error {
		select {
		case <-time.After(15 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		return callback(&models.AgentEvent{Type: "run.completed", RunID: "agent-run-1", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)})
	}}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = time.Millisecond
	worker.cancelPoll = time.Hour

	worker.execute(context.Background(), run)

	require.Equal(t, models.RunStatusCompleted, runs.finishedStatus)
	require.Greater(t, runs.heartbeatCalls.Load(), int64(1))
}

func TestRunWorkerRechecksLeaseAfterPersistingRuntime(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-1", OwnerID: "user-1", ProjectID: "project-1", WorkerID: "worker-1", InputMessage: "build", Status: models.RunStatusRunning, CreatedAt: now}
	runs := &runRepoStub{run: run}
	runs.heartbeat = func(context.Context, string, string, time.Time) (bool, error) {
		return runs.upserts.Load() == 0, nil
	}
	gateway := &gatewayStub{}
	worker := NewRunWorker(runs, gateway)
	worker.workerID = "worker-1"
	worker.heartbeat = time.Hour
	worker.cancelPoll = time.Hour

	worker.execute(context.Background(), run)

	require.Equal(t, int64(1), runs.upserts.Load())
	require.Zero(t, gateway.streamCalls)
}

type taskDeliveryStub struct {
	id       string
	ackCalls atomic.Int64
	failAck  int64
}

func (d *taskDeliveryStub) ID() string { return d.id }
func (d *taskDeliveryStub) Ack(context.Context) error {
	call := d.ackCalls.Add(1)
	if call <= d.failAck {
		return errors.New("offset commit unavailable")
	}
	return nil
}

type taskQueueStub struct {
	delivery TaskDelivery
	received atomic.Int64
}

type sequenceTaskQueueStub struct {
	deliveries []TaskDelivery
	index      atomic.Int64
}

func (q *sequenceTaskQueueStub) Receive(ctx context.Context) (TaskDelivery, error) {
	index := int(q.index.Add(1) - 1)
	if index < len(q.deliveries) {
		return q.deliveries[index], nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (q *taskQueueStub) Receive(ctx context.Context) (TaskDelivery, error) {
	if q.received.Add(1) == 1 {
		return q.delivery, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunWorkerRetriesClaimAndAckWithoutLosingExecution(t *testing.T) {
	now := time.Now().UTC()
	run := &models.Run{ID: "run-queue", OwnerID: "user-1", ProjectID: "project-1", InputMessage: "build", Status: models.RunStatusQueued, CreatedAt: now}
	runs := &runRepoStub{run: run, runtime: &models.ProjectRuntime{ProjectID: run.ProjectID, OwnerID: run.OwnerID, GatewaySessionID: "session-1", AgentConversationID: run.ProjectID, Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	var claimCalls atomic.Int64
	runs.claim = func(_ context.Context, runID, workerID string, _ time.Time) (*models.Run, error) {
		if claimCalls.Add(1) == 1 {
			return nil, errors.New("redis unavailable")
		}
		require.Equal(t, run.ID, runID)
		run.Status, run.WorkerID = models.RunStatusRunning, workerID
		return run, nil
	}
	delivery := &taskDeliveryStub{id: run.ID, failAck: 1}
	queue := &taskQueueStub{delivery: delivery}
	gateway := &gatewayStub{events: []*models.AgentEvent{{Type: "run.completed", RunID: "agent-run", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)}}}
	worker := NewRunWorker(runs, gateway, queue)
	worker.workerID = "worker-1"
	worker.orphanAge = time.Hour
	worker.heartbeat = time.Hour
	worker.cancelPoll = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	require.Eventually(t, runs.finished.Load, 3*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.Equal(t, int64(2), claimCalls.Load())
	require.Equal(t, int64(2), delivery.ackCalls.Load())
	require.Equal(t, 1, gateway.streamCalls)
}

func TestRunWorkerDoesNotSkipBlockedDeliveryBeforeTheNextTask(t *testing.T) {
	now := time.Now().UTC()
	runsByID := map[string]*models.Run{
		"run-first":  {ID: "run-first", OwnerID: "user-1", ProjectID: "project-1", InputMessage: "first", Status: models.RunStatusQueued, CreatedAt: now},
		"run-second": {ID: "run-second", OwnerID: "user-1", ProjectID: "project-2", InputMessage: "second", Status: models.RunStatusQueued, CreatedAt: now},
	}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{OwnerID: "user-1", GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	var firstClaims atomic.Int64
	runs.claim = func(_ context.Context, runID, workerID string, _ time.Time) (*models.Run, error) {
		if runID == "run-first" && firstClaims.Add(1) <= 2 {
			return nil, errors.New("redis unavailable")
		}
		run := runsByID[runID]
		run.Status, run.WorkerID = models.RunStatusRunning, workerID
		runs.run = run
		runs.runtime.ProjectID = run.ProjectID
		return run, nil
	}
	firstDelivery, secondDelivery := &taskDeliveryStub{id: "run-first"}, &taskDeliveryStub{id: "run-second"}
	queue := &sequenceTaskQueueStub{deliveries: []TaskDelivery{firstDelivery, secondDelivery}}
	gateway := &gatewayStub{events: []*models.AgentEvent{{Type: "run.completed", RunID: "agent-run", Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)}}}
	worker := NewRunWorker(runs, gateway, queue)
	worker.workerID, worker.parallel = "worker-1", 1
	worker.orphanAge, worker.heartbeat, worker.cancelPoll = time.Hour, time.Hour, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return runs.finishedCount.Load() == 2 }, 4*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.Equal(t, int64(3), firstClaims.Load())
	require.Equal(t, int64(1), firstDelivery.ackCalls.Load())
	require.Equal(t, int64(1), secondDelivery.ackCalls.Load())
	require.Equal(t, 2, gateway.streamCalls)
}
