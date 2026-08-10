package biz

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultProjectPageSize = 20
	maxProjectPageSize     = 100
	projectStatusDraft     = "DRAFT"
	defaultMessageLimit    = 100
	maxMessageLimit        = 200
	runtimeIdleTimeout     = 15 * time.Minute
)

var (
	ErrRunNotFound         = errors.New("run not found")
	ErrActiveRun           = errors.New("project already has an active run")
	ErrIdempotencyConflict = errors.New("idempotency key was used with another message")
	ErrRuntimeExpired      = errors.New("project runtime expired")
	ErrRuntimeUnavailable  = errors.New("project runtime unavailable")
	ErrPreviewNotFound     = errors.New("preview not found")
	ErrRunLeaseLost        = errors.New("run worker lease lost")
	ErrRunCancelled        = errors.New("run was cancelled before agent start")
	ErrWorkerLeaseBusy     = errors.New("worker lease is already owned")
	ErrPublicationNotFound = errors.New("publication not found")
	ErrActivePublication   = errors.New("project already has an active publication")
)

var planProjectLimits = map[string]int{"free": 12, "pro": 100, "enterprise": 1000}

type ProjectRepo interface {
	CreateProject(context.Context, *models.CreateProjectInput) (*models.Project, error)
	ListProjects(context.Context, *models.ProjectListFilter) ([]*models.Project, int, error)
	GetProjectByID(context.Context, string, string) (*models.Project, error)
	GetProjectAndTouch(context.Context, string, string, time.Time) (*models.Project, error)
	UpdateProject(context.Context, *models.UpdateProjectInput) (*models.Project, error)
	SoftDeleteProject(context.Context, string, string, time.Time) error
	CountActiveProjectsByOwner(context.Context, string) (int, error)
	GetUserPlan(context.Context, string) (string, error)
}

type RunRepo interface {
	CreateRun(context.Context, *models.CreateRunInput) (*models.Run, bool, error)
	FindRunByIdempotency(context.Context, string, string, string, string) (*models.Run, error)
	GetRun(context.Context, string, string) (*models.Run, error)
	GetProjectRunState(context.Context, string, string) (*models.ProjectRunState, error)
	ListMessages(context.Context, string, string, string, int) ([]*models.Message, *string, error)
	RequestCancel(context.Context, string, string, time.Time) (*models.Run, bool, error)
	FailRunDispatch(context.Context, string, time.Time, error) error
	GetRuntime(context.Context, string, string) (*models.ProjectRuntime, error)
	ExpireRuntime(context.Context, string, string, time.Time) error
	TouchRuntime(context.Context, string, time.Time) error
	TouchRuntimeByPreviewToken(context.Context, string, time.Time) error
	SavePreview(context.Context, *models.ProjectPreview) error
	GetPreview(context.Context, string, string) (*models.ProjectPreview, error)
}

type RunWorkerRepo interface {
	RunRepo
	GetRunForExecution(context.Context, string) (*models.Run, error)
	UpsertRuntime(context.Context, *models.ProjectRuntime) error
	AcquireRunOwnership(context.Context, string, string) (bool, error)
	RenewRunOwnership(context.Context, string, string) (bool, error)
	ReleaseRunOwnership(context.Context, string, string) (bool, error)
	ExpiredRunOwnerships(context.Context, time.Time, int64) ([]models.WorkerOwnership, error)
	TakeoverRunOwnership(context.Context, string, string, string) (bool, error)
	IsCancelRequested(context.Context, string) (bool, error)
	TouchRuntime(context.Context, string, time.Time) error
}

type TaskDelivery interface {
	ID() string
	Ack(context.Context) error
}

type TaskQueue interface {
	Receive(context.Context) (TaskDelivery, error)
}

type TaskPublisher interface {
	PublishRunTask(context.Context, string, string) error
	PublishPublicationTask(context.Context, string, string) error
}

type RunEventPublisher interface {
	PublishRunEvent(context.Context, *models.AgentEvent) error
}

type RunEventStore interface {
	Read(context.Context, string, string, time.Duration) ([]*models.StoredRunEvent, error)
}

type AgentlandGateway interface {
	EnsureRuntime(context.Context, string) (string, error)
	StreamChat(context.Context, string, string, string, func(*models.AgentEvent) error) error
	CancelRun(context.Context, string, string) error
	GetFileTree(context.Context, string, string) (*models.GatewayFileTree, error)
	GetFile(context.Context, string, string) (*models.GatewayFile, error)
	PutFile(context.Context, string, string, string, string) (*models.GatewayFileWrite, error)
	CreatePreview(context.Context, string, int) (*models.GatewayPreviewInfo, error)
}

type AsyncAgentlandGateway interface {
	AgentlandGateway
	StartAgentRun(context.Context, string, string, string, string) (*models.AgentRunState, error)
	GetAgentRun(context.Context, string, string) (*models.AgentRunState, error)
	StreamAgentRun(context.Context, string, string, int64, func(*models.AgentEvent) error) error
}

type projectUseCase struct {
	projects     ProjectRepo
	runs         RunRepo
	events       RunEventStore
	gateway      AgentlandGateway
	publications PublicationRepo
	publisher    PublicationGateway
	evaluator    EvaluationSink
	tasks        TaskPublisher
	now          func() time.Time
}

func NewProjectUsecaseWithPublishing(projects ProjectRepo, runs RunRepo, events RunEventStore, gateway AgentlandGateway, publications PublicationRepo, publisher PublicationGateway, evaluators ...EvaluationSink) ProjectUseCase {
	usecase := NewProjectUsecase(projects, runs, events, gateway, evaluators...).(*projectUseCase)
	usecase.publications = publications
	usecase.publisher = publisher
	return usecase
}

func NewProjectUsecaseWithPublishingAndTasks(projects ProjectRepo, runs RunRepo, events RunEventStore, gateway AgentlandGateway, publications PublicationRepo, publisher PublicationGateway, tasks TaskPublisher, evaluators ...EvaluationSink) ProjectUseCase {
	usecase := NewProjectUsecaseWithPublishing(projects, runs, events, gateway, publications, publisher, evaluators...).(*projectUseCase)
	usecase.tasks = tasks
	return usecase
}

func NewProjectUsecase(projects ProjectRepo, runs RunRepo, events RunEventStore, gateway AgentlandGateway, evaluators ...EvaluationSink) ProjectUseCase {
	var evaluator EvaluationSink
	if len(evaluators) != 0 {
		evaluator = evaluators[0]
	}
	return &projectUseCase{projects: projects, runs: runs, events: events, gateway: gateway, evaluator: evaluator, now: time.Now}
}

func (u *projectUseCase) List(ctx context.Context, principal models.AuthPrincipal, req *models.ProjectListReq) (*models.ProjectListResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	view := strings.ToLower(strings.TrimSpace(req.View))
	if view == "" {
		view = "all"
	}
	if view != "all" && view != "recent" && view != "shared" {
		return nil, response.InvalidArgumentError("view", "unsupported")
	}
	page, pageSize := normalizePagination(req.Page, req.PageSize)
	if view == "shared" {
		return &models.ProjectListResp{Items: []models.ProjectItem{}, Pagination: models.Pagination{Page: page, PageSize: pageSize}}, nil
	}
	sortBy := strings.ToLower(strings.TrimSpace(req.SortBy))
	if sortBy == "" {
		sortBy = "updated_at"
	}
	if sortBy != "updated_at" && sortBy != "created_at" && sortBy != "name" {
		return nil, response.InvalidArgumentError("sort_by", "unsupported")
	}
	sortOrder := strings.ToLower(strings.TrimSpace(req.SortOrder))
	if sortOrder == "" {
		sortOrder = "desc"
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		return nil, response.InvalidArgumentError("sort_order", "unsupported")
	}
	projects, total, err := u.projects.ListProjects(ctx, &models.ProjectListFilter{OwnerID: principal.UserID, Keyword: strings.TrimSpace(req.Keyword), Status: strings.ToUpper(strings.TrimSpace(req.Status)), SortBy: sortBy, SortOrder: sortOrder, Page: page, PageSize: pageSize, View: view})
	if err != nil {
		return nil, mapAPIError(err)
	}
	items := make([]models.ProjectItem, 0, len(projects))
	for _, project := range projects {
		items = append(items, models.ProjectItem{ID: project.ID, Name: project.Name, Status: project.Status, ThumbnailURL: project.ThumbnailURL, CreatedAt: formatTime(project.CreatedAt), UpdatedAt: formatTime(project.UpdatedAt)})
	}
	return &models.ProjectListResp{Items: items, Pagination: models.Pagination{Page: page, PageSize: pageSize, Total: total}}, nil
}

func (u *projectUseCase) Create(ctx context.Context, principal models.AuthPrincipal, req *models.ProjectCreateReq) (*models.ProjectCreateResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	name, template := strings.TrimSpace(req.Name), strings.TrimSpace(req.Template)
	if name == "" {
		return nil, response.InvalidArgumentError("name", "required")
	}
	if template == "" {
		return nil, response.InvalidArgumentError("template", "required")
	}
	now := u.now().UTC()
	project, err := u.projects.CreateProject(ctx, &models.CreateProjectInput{ID: token.NewID("p"), OwnerID: principal.UserID, Name: name, Template: template, Status: projectStatusDraft, Now: now})
	if err != nil {
		return nil, mapAPIError(err)
	}
	return &models.ProjectCreateResp{ID: project.ID, Name: project.Name, Status: project.Status, CreatedAt: formatTime(project.CreatedAt)}, nil
}

func (u *projectUseCase) Detail(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.ProjectDetailResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	projectID = strings.TrimSpace(projectID)
	project, err := u.projects.GetProjectAndTouch(ctx, principal.UserID, projectID, u.now().UTC())
	if err != nil {
		return nil, mapAPIError(err)
	}
	state, err := u.runs.GetProjectRunState(ctx, principal.UserID, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	runtime, err := u.runs.GetRuntime(ctx, principal.UserID, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	runtimeStatus := models.RuntimeStatusUnavailable
	if runtime != nil {
		runtimeStatus = runtime.Status
		if runtimeIsExpired(runtime, u.now().UTC()) {
			runtimeStatus = models.RuntimeStatusExpired
			_ = u.runs.ExpireRuntime(ctx, principal.UserID, projectID, u.now().UTC())
		}
	}
	return &models.ProjectDetailResp{ID: project.ID, Name: project.Name, Status: project.Status, OwnerID: project.OwnerID, LastOpenedAt: formatTimePointer(project.LastOpenedAt), Metadata: metadataPointer(project.Metadata), RuntimeStatus: runtimeStatus, ActiveRunID: state.ActiveRunID, LastRunID: state.LastRunID}, nil
}

func (u *projectUseCase) Update(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.ProjectUpdateReq) (*models.ProjectUpdateResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	hasName := strings.TrimSpace(req.Name) != ""
	hasMetadata := req.Metadata != nil && strings.TrimSpace(req.Metadata.LastViewMode) != ""
	if !hasName && !hasMetadata {
		return nil, response.InvalidArgumentError("request", "name or metadata.last_view_mode required")
	}
	if hasMetadata && req.Metadata.LastViewMode != "preview" && req.Metadata.LastViewMode != "code" {
		return nil, response.InvalidArgumentError("metadata.last_view_mode", "unsupported")
	}
	existing, err := u.projects.GetProjectByID(ctx, principal.UserID, strings.TrimSpace(projectID))
	if err != nil {
		return nil, mapAPIError(err)
	}
	name, metadata := existing.Name, existing.Metadata
	if hasName {
		name = strings.TrimSpace(req.Name)
	}
	if hasMetadata {
		metadata.LastViewMode = strings.TrimSpace(req.Metadata.LastViewMode)
	}
	project, err := u.projects.UpdateProject(ctx, &models.UpdateProjectInput{ProjectID: existing.ID, OwnerID: principal.UserID, Name: name, Metadata: metadata, Now: u.now().UTC()})
	if err != nil {
		return nil, mapAPIError(err)
	}
	return &models.ProjectUpdateResp{ID: project.ID, Name: project.Name, UpdatedAt: formatTime(project.UpdatedAt), Metadata: metadataPointer(project.Metadata)}, nil
}

func (u *projectUseCase) Delete(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.ProjectDeleteResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if err := u.projects.SoftDeleteProject(ctx, principal.UserID, strings.TrimSpace(projectID), u.now().UTC()); err != nil {
		return nil, mapAPIError(err)
	}
	return &models.ProjectDeleteResp{Success: true}, nil
}

func (u *projectUseCase) Usage(ctx context.Context, principal models.AuthPrincipal) (*models.ProjectUsageResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	used, err := u.projects.CountActiveProjectsByOwner(ctx, principal.UserID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	plan, err := u.projects.GetUserPlan(ctx, principal.UserID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	limit := planProjectLimits[strings.ToLower(strings.TrimSpace(plan))]
	if limit == 0 {
		limit = planProjectLimits["free"]
	}
	return &models.ProjectUsageResp{Used: used, Limit: limit}, nil
}

func (u *projectUseCase) CreateRun(ctx context.Context, principal models.AuthPrincipal, projectID, idempotencyKey string, req *models.RunCreateReq) (*models.RunCreateResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if len(req.Message) > models.MaxRunMessageBytes {
		return nil, response.InvalidArgumentError("message", "too large")
	}
	projectID, idempotencyKey = strings.TrimSpace(projectID), strings.TrimSpace(idempotencyKey)
	message := strings.TrimSpace(req.Message)
	if idempotencyKey == "" {
		return nil, response.InvalidArgumentError("Idempotency-Key", "required")
	}
	if message == "" {
		return nil, response.InvalidArgumentError("message", "required")
	}
	if _, err := u.projects.GetProjectByID(ctx, principal.UserID, projectID); err != nil {
		return nil, mapAPIError(err)
	}
	if existing, err := u.runs.FindRunByIdempotency(ctx, principal.UserID, projectID, idempotencyKey, message); err != nil {
		return nil, mapAPIError(err)
	} else if existing != nil {
		if u.tasks != nil && existing.Status == models.RunStatusRunning {
			if err = u.tasks.PublishRunTask(ctx, existing.ID, existing.ProjectID); err != nil {
				return nil, mapAPIError(err)
			}
		}
		return &models.RunCreateResp{RunID: existing.ID, UserMessageID: existing.InputMessageID, Status: existing.Status, CreatedAt: formatTime(existing.CreatedAt)}, nil
	}
	if runtime, err := u.runs.GetRuntime(ctx, principal.UserID, projectID); err != nil {
		return nil, mapAPIError(err)
	} else if runtime != nil && runtimeIsExpired(runtime, u.now().UTC()) {
		_ = u.runs.ExpireRuntime(ctx, principal.UserID, projectID, u.now().UTC())
		return nil, response.ProjectRuntimeExpiredError()
	}
	now := u.now().UTC()
	traceCarrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, traceCarrier)
	run, existing, err := u.runs.CreateRun(ctx, &models.CreateRunInput{
		ID: token.NewID("run"), OwnerID: principal.UserID, ProjectID: projectID, IdempotencyKey: idempotencyKey,
		InputMessageID: token.NewID("msg"), AssistantMessageID: token.NewID("msg"), Message: message,
		TraceParent: traceCarrier.Get("traceparent"), TraceState: traceCarrier.Get("tracestate"), Now: now,
	})
	if err != nil {
		return nil, mapAPIError(err)
	}
	if u.tasks != nil {
		if err = u.tasks.PublishRunTask(ctx, run.ID, run.ProjectID); err != nil {
			if !existing {
				_ = u.runs.FailRunDispatch(context.WithoutCancel(ctx), run.ID, u.now().UTC(), err)
			}
			return nil, mapAPIError(err)
		}
	}
	return &models.RunCreateResp{RunID: run.ID, UserMessageID: run.InputMessageID, Status: run.Status, CreatedAt: formatTime(run.CreatedAt)}, nil
}

func (u *projectUseCase) GetRun(ctx context.Context, principal models.AuthPrincipal, runID string) (*models.RunResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	run, err := u.runs.GetRun(ctx, principal.UserID, strings.TrimSpace(runID))
	if err != nil {
		return nil, mapAPIError(err)
	}
	return runResponse(run), nil
}

func (u *projectUseCase) StreamRunEvents(ctx context.Context, principal models.AuthPrincipal, runID, after string, send func(*models.StoredRunEvent) error) *response.APIError {
	if !authorized(principal) {
		return response.UnauthorizedError()
	}
	run, err := u.runs.GetRun(ctx, principal.UserID, strings.TrimSpace(runID))
	if err != nil {
		return mapAPIError(err)
	}
	if strings.TrimSpace(after) == "" {
		after = "0-0"
	}
	for {
		events, readErr := u.events.Read(ctx, run.ID, after, 5*time.Second)
		if readErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return response.InternalError()
		}
		for _, event := range events {
			after = event.ID
			if send != nil {
				if err = send(event); err != nil {
					return nil
				}
			}
			if isTerminalEvent(event.Type) {
				return nil
			}
		}
		if len(events) == 0 {
			run, err = u.runs.GetRun(ctx, principal.UserID, runID)
			if err != nil {
				return nil
			}
			if isTerminalStatus(run.Status) {
				if send != nil {
					_ = send(syntheticTerminalEvent(run))
				}
				return nil
			}
			if send != nil {
				if err = send(&models.StoredRunEvent{}); err != nil {
					return nil
				}
			}
		}
	}
}

func (u *projectUseCase) CancelRun(ctx context.Context, principal models.AuthPrincipal, runID string) (*models.RunCancelResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	run, _, err := u.runs.RequestCancel(ctx, principal.UserID, strings.TrimSpace(runID), u.now().UTC())
	if err != nil {
		return nil, mapAPIError(err)
	}
	if run.Status != models.RunStatusCancelled && run.AgentRunID != "" {
		runtime, runtimeErr := u.runs.GetRuntime(ctx, principal.UserID, run.ProjectID)
		if runtimeErr != nil {
			return nil, mapAPIError(runtimeErr)
		}
		if runtime != nil {
			if cancelErr := u.gateway.CancelRun(ctx, runtime.GatewaySessionID, run.AgentRunID); cancelErr != nil {
				return nil, gatewayAPIError(cancelErr)
			}
		}
	}
	return &models.RunCancelResp{ID: run.ID, Status: run.Status}, nil
}

func (u *projectUseCase) RunTrajectory(ctx context.Context, principal models.AuthPrincipal, runID string) (*models.RunTrajectoryResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	run, err := u.runs.GetRun(ctx, principal.UserID, strings.TrimSpace(runID))
	if err != nil {
		return nil, mapAPIError(err)
	}
	artifacts, ok := u.runs.(RunArtifactRepo)
	if !ok {
		return nil, response.InternalError()
	}
	records, err := artifacts.LoadTrajectoryRecords(ctx, run.ID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	if len(records) == 0 {
		return nil, response.ReplayUnavailableError("trajectory is unavailable")
	}
	return &models.RunTrajectoryResp{RunID: run.ID, Records: records}, nil
}

func (u *projectUseCase) ReplayRun(ctx context.Context, principal models.AuthPrincipal, runID string, req *models.ReplayRunReq) (*models.ReplayRunResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if req == nil {
		return nil, response.InvalidArgumentError("request", "required")
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != models.ReplayModeDecision && mode != models.ReplayModeLive {
		return nil, response.InvalidArgumentError("mode", "must be decision or live")
	}
	run, err := u.runs.GetRun(ctx, principal.UserID, strings.TrimSpace(runID))
	if err != nil {
		return nil, mapAPIError(err)
	}
	if !isTerminalStatus(run.Status) {
		return nil, response.ReplayUnavailableError("run is still active")
	}
	artifacts, repoOK := u.runs.(RunArtifactRepo)
	replayGateway, gatewayOK := u.gateway.(ReplayGateway)
	if !repoOK || !gatewayOK {
		return nil, response.InternalError()
	}
	records, err := artifacts.LoadTrajectoryRecords(ctx, run.ID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	if len(records) == 0 {
		return nil, response.ReplayUnavailableError("trajectory is unavailable")
	}

	replayID := token.NewID("replay")
	replayInputJSON, _ := json.Marshal(map[string]string{"source_run_id": run.ID, "mode": mode})
	replayCtx, span := otel.Tracer("agentland/app-be/replay").Start(ctx, "agent.replay",
		trace.WithNewRoot(),
		trace.WithLinks(trace.LinkFromContext(ctx)),
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "evaluate"),
			attribute.String("langfuse.observation.type", "evaluator"),
			attribute.String("langfuse.trace.name", "agent-replay"),
			attribute.String("langfuse.session.id", run.ProjectID),
			attribute.String("agent.replay.id", replayID),
			attribute.String("agent.replay.mode", mode),
			attribute.String("agent.source_run.id", run.ID),
			attribute.Int("agent.trajectory_records", len(records)),
			attribute.String("langfuse.observation.input", string(replayInputJSON)),
		),
	)
	defer span.End()
	var snapshot *models.WorkspaceSnapshot
	if mode == models.ReplayModeLive {
		snapshot, err = artifacts.LoadWorkspaceSnapshot(replayCtx, run.ID)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, mapAPIError(err)
		}
		if snapshot == nil || snapshot.Error != "" || len(snapshot.Data) == 0 {
			reason := "workspace snapshot is unavailable"
			if snapshot != nil && snapshot.Error != "" {
				reason = snapshot.Error
			}
			span.SetStatus(codes.Error, reason)
			return nil, response.ReplayUnavailableError(reason)
		}
	}
	sessionID, err := u.gateway.EnsureRuntime(replayCtx, "")
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, gatewayAPIError(err)
	}

	var report *models.ReplayRunResp
	if mode == models.ReplayModeDecision {
		report, err = replayGateway.ReplayDecisions(replayCtx, sessionID, records)
	} else {
		err = replayGateway.RestoreWorkspaceSnapshot(replayCtx, sessionID, snapshot.Data)
		if err == nil {
			report, err = replayGateway.ReplayLive(replayCtx, sessionID, records)
		}
		if err == nil {
			var outputSnapshot []byte
			outputSnapshot, err = replayGateway.GetWorkspaceSnapshot(replayCtx, sessionID)
			if err == nil {
				digest := sha256.Sum256(outputSnapshot)
				report.SourceSnapshotSHA = snapshot.SHA
				report.OutputSnapshotSHA = fmt.Sprintf("%x", digest[:])
				report.WorkspaceChanged = report.OutputSnapshotSHA != report.SourceSnapshotSHA
			}
		}
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, gatewayAPIError(err)
	}
	report.ID, report.SourceRunID, report.Mode = replayID, run.ID, mode
	reportJSON, _ := json.Marshal(report)
	span.SetAttributes(
		attribute.Float64("agent.replay.score", report.Score),
		attribute.Int("agent.replay.total_steps", report.TotalSteps),
		attribute.Int("agent.replay.matched_steps", report.MatchedSteps),
		attribute.String("agent.replay.status", report.Status),
		attribute.String("langfuse.observation.output", string(reportJSON)),
	)
	if u.evaluator != nil {
		if scoreErr := u.evaluator.ScoreReplay(replayCtx, span.SpanContext().TraceID().String(), report); scoreErr != nil {
			span.AddEvent("langfuse.score.failed", trace.WithAttributes(attribute.String("error.message", scoreErr.Error())))
		}
	}
	return report, nil
}

func (u *projectUseCase) ListMessages(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.MessageListReq) (*models.MessageListResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	projectID = strings.TrimSpace(projectID)
	if _, err := u.projects.GetProjectByID(ctx, principal.UserID, projectID); err != nil {
		return nil, mapAPIError(err)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultMessageLimit
	}
	if limit > maxMessageLimit {
		limit = maxMessageLimit
	}
	messages, next, err := u.runs.ListMessages(ctx, principal.UserID, projectID, strings.TrimSpace(req.Cursor), limit)
	if err != nil {
		return nil, mapAPIError(err)
	}
	items := make([]models.MessageItem, 0, len(messages))
	for _, message := range messages {
		items = append(items, models.MessageItem{ID: message.ID, RunID: message.RunID, Role: message.Role, Content: message.Content, Status: message.Status, CreatedAt: formatTime(message.CreatedAt), UpdatedAt: formatTime(message.UpdatedAt)})
	}
	return &models.MessageListResp{Items: items, NextCursor: next}, nil
}

func (u *projectUseCase) FileTree(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.FileTreeReq) (*models.FileTreeResp, *response.APIError) {
	runtime, apiErr := u.workspaceRuntime(ctx, principal, projectID)
	if apiErr != nil {
		return nil, apiErr
	}
	tree, err := u.gateway.GetFileTree(ctx, runtime.GatewaySessionID, strings.TrimSpace(req.Path))
	if err != nil {
		return nil, u.workspaceGatewayError(ctx, runtime, err)
	}
	_ = u.runs.TouchRuntime(ctx, strings.TrimSpace(projectID), u.now().UTC())
	return &models.FileTreeResp{Root: tree.Root, Nodes: tree.Nodes}, nil
}

func (u *projectUseCase) FileContent(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.FileContentReq) (*models.FileContentResp, *response.APIError) {
	runtime, apiErr := u.workspaceRuntime(ctx, principal, projectID)
	if apiErr != nil {
		return nil, apiErr
	}
	file, err := u.gateway.GetFile(ctx, runtime.GatewaySessionID, strings.TrimSpace(req.Path))
	if err != nil {
		return nil, u.workspaceGatewayError(ctx, runtime, err)
	}
	_ = u.runs.TouchRuntime(ctx, strings.TrimSpace(projectID), u.now().UTC())
	return &models.FileContentResp{Path: file.Path, Size: file.Size, Content: file.Content, SHA: file.SHA}, nil
}

func (u *projectUseCase) UpdateFileContent(ctx context.Context, principal models.AuthPrincipal, projectID string, query *models.FileContentReq, req *models.FileContentUpdateReq) (*models.FileContentUpdateResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if req.SHA == nil {
		return nil, response.InvalidArgumentError("sha", "required")
	}
	if len(req.Content) > models.MaxFileContentBytes {
		return nil, response.InvalidArgumentError("content", "too large")
	}
	if publications, ok := u.runs.(interface {
		HasPreparingPublication(context.Context, string, string) (bool, error)
	}); ok {
		active, err := publications.HasPreparingPublication(ctx, principal.UserID, strings.TrimSpace(projectID))
		if err != nil {
			return nil, mapAPIError(err)
		}
		if active {
			return nil, response.ActivePublicationConflictError()
		}
	}
	runtime, apiErr := u.workspaceRuntime(ctx, principal, projectID)
	if apiErr != nil {
		return nil, apiErr
	}
	file, err := u.gateway.PutFile(ctx, runtime.GatewaySessionID, strings.TrimSpace(query.Path), req.Content, strings.TrimSpace(*req.SHA))
	if err != nil {
		return nil, u.workspaceGatewayError(ctx, runtime, err)
	}
	_ = u.runs.TouchRuntime(ctx, strings.TrimSpace(projectID), u.now().UTC())
	return &models.FileContentUpdateResp{Path: file.Path, Size: file.Size, SHA: file.SHA}, nil
}

func (u *projectUseCase) StartPreview(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.PreviewStartReq) (*models.PreviewResp, *response.APIError) {
	runtime, apiErr := u.workspaceRuntime(ctx, principal, projectID)
	if apiErr != nil {
		return nil, apiErr
	}
	preview, err := u.gateway.CreatePreview(ctx, runtime.GatewaySessionID, req.Port)
	if err != nil {
		return nil, u.workspaceGatewayError(ctx, runtime, err)
	}
	now := u.now().UTC()
	record := &models.ProjectPreview{ID: token.NewID("preview"), ProjectID: strings.TrimSpace(projectID), OwnerID: principal.UserID, Status: "running", PreviewURL: preview.PreviewURL, PreviewToken: preview.PreviewToken, Port: preview.Port, CreatedAt: now, LastActiveAt: now, ExpiresAt: preview.ExpiresAt, UpdatedAt: now}
	if err = u.runs.SavePreview(ctx, record); err != nil {
		return nil, mapAPIError(err)
	}
	_ = u.runs.TouchRuntime(ctx, strings.TrimSpace(projectID), now)
	return previewResponse(record), nil
}

func (u *projectUseCase) Preview(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.PreviewResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	projectID = strings.TrimSpace(projectID)
	if _, err := u.projects.GetProjectByID(ctx, principal.UserID, projectID); err != nil {
		return nil, mapAPIError(err)
	}
	runtime, err := u.runs.GetRuntime(ctx, principal.UserID, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	preview, err := u.runs.GetPreview(ctx, principal.UserID, projectID)
	if errors.Is(err, ErrPreviewNotFound) {
		return &models.PreviewResp{Status: "idle"}, nil
	}
	if err != nil {
		return nil, mapAPIError(err)
	}
	if runtime == nil || runtimeIsExpired(runtime, u.now().UTC()) || !preview.ExpiresAt.After(u.now().UTC()) {
		preview.Status = "expired"
	}
	return previewResponse(preview), nil
}

func (u *projectUseCase) workspaceRuntime(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.ProjectRuntime, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	projectID = strings.TrimSpace(projectID)
	if _, err := u.projects.GetProjectByID(ctx, principal.UserID, projectID); err != nil {
		return nil, mapAPIError(err)
	}
	runtime, err := u.runs.GetRuntime(ctx, principal.UserID, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	if runtime == nil {
		return nil, response.RuntimeUnavailableError()
	}
	if runtimeIsExpired(runtime, u.now().UTC()) {
		_ = u.runs.ExpireRuntime(ctx, principal.UserID, projectID, u.now().UTC())
		return nil, response.ProjectRuntimeExpiredError()
	}
	return runtime, nil
}

func (u *projectUseCase) workspaceGatewayError(ctx context.Context, runtime *models.ProjectRuntime, err error) *response.APIError {
	if gatewayRuntimeExpired(err) {
		_ = u.runs.ExpireRuntime(ctx, runtime.OwnerID, runtime.ProjectID, u.now().UTC())
		return response.ProjectRuntimeExpiredError()
	}
	return gatewayAPIError(err)
}

func authorized(principal models.AuthPrincipal) bool {
	return strings.TrimSpace(principal.UserID) != ""
}

func runtimeIsExpired(runtime *models.ProjectRuntime, now time.Time) bool {
	return runtime.Status == models.RuntimeStatusExpired ||
		!runtime.ExpiresAt.After(now) ||
		!runtime.LastActiveAt.Add(configDuration("runtime.idle_timeout", runtimeIdleTimeout)).After(now)
}

func normalizePagination(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = defaultProjectPageSize
	}
	if pageSize > maxProjectPageSize {
		pageSize = maxProjectPageSize
	}
	return page, pageSize
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatTimePointer(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTime(*value)
}

func optionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func metadataPointer(metadata models.ProjectMetadata) *models.ProjectMetadata {
	if strings.TrimSpace(metadata.LastViewMode) == "" {
		return nil
	}
	copy := metadata
	return &copy
}

func runResponse(run *models.Run) *models.RunResp {
	return &models.RunResp{ID: run.ID, ProjectID: run.ProjectID, Status: run.Status, InputMessageID: run.InputMessageID, AssistantMessageID: run.AssistantMessageID, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage, LastSequence: run.LastSequence, CancelRequestedAt: optionalTime(run.CancelRequestedAt), CreatedAt: formatTime(run.CreatedAt), StartedAt: optionalTime(run.StartedAt), CompletedAt: optionalTime(run.CompletedAt)}
}

func previewResponse(preview *models.ProjectPreview) *models.PreviewResp {
	return &models.PreviewResp{PreviewID: preview.ID, Status: preview.Status, PreviewURL: preview.PreviewURL, Port: preview.Port, ExpiresAt: formatTime(preview.ExpiresAt), LastHeartbeatAt: formatTime(preview.LastActiveAt)}
}

func isTerminalStatus(status string) bool {
	return status == models.RunStatusCompleted || status == models.RunStatusFailed || status == models.RunStatusCancelled
}

func isTerminalEvent(eventType string) bool {
	return eventType == "run.completed" || eventType == "run.failed" || eventType == "run.cancelled"
}

func syntheticTerminalEvent(run *models.Run) *models.StoredRunEvent {
	eventType := "run.completed"
	payload := json.RawMessage(`{}`)
	if run.Status == models.RunStatusFailed {
		eventType = "run.failed"
		payload, _ = json.Marshal(map[string]string{"code": run.ErrorCode, "error": run.ErrorMessage})
	} else if run.Status == models.RunStatusCancelled {
		eventType = "run.cancelled"
	}
	timestamp := run.UpdatedAt
	if run.CompletedAt != nil {
		timestamp = *run.CompletedAt
	}
	data, _ := json.Marshal(&models.AgentEvent{Type: eventType, RunID: run.ID, ConversationID: run.ProjectID, Sequence: run.LastSequence, Timestamp: timestamp, Payload: payload})
	return &models.StoredRunEvent{ID: strconv.FormatInt(run.LastSequence, 10), Type: eventType, Data: data}
}

func mapAPIError(err error) *response.APIError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, autherr.ErrProjectNotFound), errors.Is(err, ErrRunNotFound), errors.Is(err, ErrPreviewNotFound), errors.Is(err, ErrPublicationNotFound):
		return response.NotFoundError()
	case errors.Is(err, autherr.ErrUserNotFound):
		return response.UnauthorizedError()
	case errors.Is(err, ErrActiveRun):
		return response.ActiveRunConflictError()
	case errors.Is(err, ErrActivePublication):
		return response.ActivePublicationConflictError()
	case errors.Is(err, ErrIdempotencyConflict):
		return response.IdempotencyConflictError()
	case errors.Is(err, ErrRuntimeExpired):
		return response.ProjectRuntimeExpiredError()
	default:
		return response.InternalError()
	}
}

func gatewayAPIError(err error) *response.APIError {
	var gatewayErr *models.GatewayResponseError
	if errors.As(err, &gatewayErr) {
		if gatewayErr.Code == "PROJECT_RUNTIME_EXPIRED" {
			return response.ProjectRuntimeExpiredError()
		}
		if gatewayErr.StatusCode == http.StatusConflict && gatewayErr.Code == "FILE_CONFLICT" {
			return response.FileConflictError(gatewayErr.SHA)
		}
		if gatewayErr.StatusCode == http.StatusNotFound {
			return response.NotFoundError()
		}
	}
	return response.RuntimeUnavailableError()
}

func gatewayRuntimeExpired(err error) bool {
	var gatewayErr *models.GatewayResponseError
	return errors.As(err, &gatewayErr) && gatewayErr.Code == "PROJECT_RUNTIME_EXPIRED"
}
