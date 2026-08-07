package biz

import (
	"context"
	"errors"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type PublicationRepo interface {
	CreatePublication(context.Context, *models.CreatePublicationInput) (*models.Publication, bool, error)
	FindPublicationByIdempotency(context.Context, string, string, string, string, string) (*models.Publication, error)
	GetPublication(context.Context, string, string) (*models.Publication, error)
	ListPublications(context.Context, string, string, int) ([]*models.Publication, error)
	RequestPublicationCancel(context.Context, string, string, time.Time) (*models.Publication, error)
}

type PublicationWorkerRepo interface {
	PublicationRepo
	ClaimNextPublication(context.Context, string, time.Time) (*models.Publication, error)
	HeartbeatPublication(context.Context, string, string, time.Time) (bool, error)
	FinishPublication(context.Context, *models.FinishPublicationInput) (bool, error)
	FailOrphanedPublications(context.Context, time.Time, time.Time) (int64, error)
	IsPublicationCancelRequested(context.Context, string) (bool, error)
	GetRuntime(context.Context, string, string) (*models.ProjectRuntime, error)
}

type PublicationGateway interface {
	PublishImage(context.Context, string, string, string, string, string) (*models.GatewayPublication, error)
}

const maxPublicationTextBytes = 1 << 20

func (u *projectUseCase) CreatePublication(ctx context.Context, principal models.AuthPrincipal, projectID, idempotencyKey string, req *models.PublicationCreateReq) (*models.PublicationResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if u.publications == nil || u.publisher == nil {
		return nil, response.InternalError()
	}
	if req == nil {
		return nil, response.InvalidArgumentError("request", "required")
	}
	projectID, idempotencyKey = strings.TrimSpace(projectID), strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return nil, response.InvalidArgumentError("Idempotency-Key", "required and limited to 128 characters")
	}
	contextPath, err := publicationPath(req.Context, ".")
	if err != nil {
		return nil, response.InvalidArgumentError("context", err.Error())
	}
	dockerfile, err := publicationPath(req.Dockerfile, "Dockerfile")
	if err != nil {
		return nil, response.InvalidArgumentError("dockerfile", err.Error())
	}
	if _, err = u.projects.GetProjectByID(ctx, principal.UserID, projectID); err != nil {
		return nil, mapAPIError(err)
	}
	if existing, findErr := u.publications.FindPublicationByIdempotency(ctx, principal.UserID, projectID, idempotencyKey, contextPath, dockerfile); findErr != nil {
		return nil, mapAPIError(findErr)
	} else if existing != nil {
		return publicationResponse(existing), nil
	}
	runtime, apiErr := u.workspaceRuntime(ctx, principal, projectID)
	if apiErr != nil {
		return nil, apiErr
	}
	if runtime == nil {
		return nil, response.RuntimeUnavailableError()
	}
	state, err := u.runs.GetProjectRunState(ctx, principal.UserID, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	if state.ActiveRunID != nil {
		return nil, response.ActiveRunConflictError()
	}
	now := u.now().UTC()
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	publication, _, err := u.publications.CreatePublication(ctx, &models.CreatePublicationInput{
		ID: token.NewID("pub"), OwnerID: principal.UserID, ProjectID: projectID, IdempotencyKey: idempotencyKey,
		Context: contextPath, Dockerfile: dockerfile, TraceParent: carrier.Get("traceparent"), TraceState: carrier.Get("tracestate"), Now: now,
	})
	if err != nil {
		return nil, mapAPIError(err)
	}
	_ = u.runs.TouchRuntime(ctx, projectID, now)
	return publicationResponse(publication), nil
}

func (u *projectUseCase) ListPublications(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.PublicationListResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if u.publications == nil {
		return nil, response.InternalError()
	}
	projectID = strings.TrimSpace(projectID)
	if _, err := u.projects.GetProjectByID(ctx, principal.UserID, projectID); err != nil {
		return nil, mapAPIError(err)
	}
	items, err := u.publications.ListPublications(ctx, principal.UserID, projectID, 50)
	if err != nil {
		return nil, mapAPIError(err)
	}
	result := make([]models.PublicationResp, 0, len(items))
	for _, item := range items {
		result = append(result, *publicationResponse(item))
	}
	return &models.PublicationListResp{Items: result}, nil
}

func (u *projectUseCase) GetPublication(ctx context.Context, principal models.AuthPrincipal, publicationID string) (*models.PublicationResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if u.publications == nil {
		return nil, response.InternalError()
	}
	publication, err := u.publications.GetPublication(ctx, principal.UserID, strings.TrimSpace(publicationID))
	if err != nil {
		return nil, mapAPIError(err)
	}
	return publicationResponse(publication), nil
}

func (u *projectUseCase) CancelPublication(ctx context.Context, principal models.AuthPrincipal, publicationID string) (*models.PublicationCancelResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if u.publications == nil {
		return nil, response.InternalError()
	}
	publication, err := u.publications.RequestPublicationCancel(ctx, principal.UserID, strings.TrimSpace(publicationID), u.now().UTC())
	if err != nil {
		return nil, mapAPIError(err)
	}
	return &models.PublicationCancelResp{ID: publication.ID, Status: publication.Status}, nil
}

func publicationPath(value, fallback string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if len(value) > 512 || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") {
		return "", errors.New("must be a relative workspace path")
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("must stay inside the workspace")
	}
	return clean, nil
}

func publicationResponse(item *models.Publication) *models.PublicationResp {
	return &models.PublicationResp{
		ID: item.ID, ProjectID: item.ProjectID, Status: item.Status, Context: item.Context, Dockerfile: item.Dockerfile,
		ImageRef: item.ImageRef, Digest: item.Digest, Logs: item.Logs, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage,
		CancelRequestedAt: optionalTime(item.CancelRequestedAt), CreatedAt: formatTime(item.CreatedAt),
		StartedAt: optionalTime(item.StartedAt), CompletedAt: optionalTime(item.CompletedAt),
	}
}

type PublicationWorker struct {
	repo       PublicationWorkerRepo
	gateway    PublicationGateway
	workerID   string
	now        func() time.Time
	poll       time.Duration
	heartbeat  time.Duration
	cancelPoll time.Duration
	orphanAge  time.Duration
	parallel   int
}

func NewPublicationWorker(repo PublicationWorkerRepo, gateway PublicationGateway) *PublicationWorker {
	return &PublicationWorker{
		repo: repo, gateway: gateway, workerID: token.NewID("publisher"), now: time.Now,
		poll:       configDuration("publication.worker.poll_interval", time.Second),
		heartbeat:  configDuration("publication.worker.heartbeat_interval", 5*time.Second),
		cancelPoll: configDuration("publication.worker.cancel_poll_interval", 250*time.Millisecond),
		orphanAge:  configDuration("publication.worker.orphan_timeout", 30*time.Second),
		parallel:   configInt("publication.worker.parallelism", 2),
	}
}

func (w *PublicationWorker) Run(ctx context.Context) {
	w.recoverOrphans(ctx)
	poll := time.NewTicker(w.poll)
	sweep := time.NewTicker(w.orphanAge)
	defer poll.Stop()
	defer sweep.Stop()
	semaphore := make(chan struct{}, w.parallel)
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			w.recoverOrphans(ctx)
		case <-poll.C:
			select {
			case semaphore <- struct{}{}:
			default:
				continue
			}
			publication, err := w.repo.ClaimNextPublication(ctx, w.workerID, w.now().UTC())
			if err != nil {
				<-semaphore
				zap.L().Warn("claim publication failed", zap.Error(err))
				continue
			}
			if publication == nil {
				<-semaphore
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-semaphore }()
				w.execute(ctx, publication)
			}()
		}
	}
}

func (w *PublicationWorker) execute(parent context.Context, item *models.Publication) {
	if item.TraceParent != "" {
		carrier := propagation.MapCarrier{"traceparent": item.TraceParent, "tracestate": item.TraceState}
		parent = propagation.TraceContext{}.Extract(parent, carrier)
	}
	ctx, span := otel.Tracer("agentland/app-be/publisher").Start(parent, "publication.execute", trace.WithAttributes(
		attribute.String("app.publication.id", item.ID), attribute.String("app.project.id", item.ProjectID),
	))
	defer span.End()
	runtime, err := w.repo.GetRuntime(ctx, item.OwnerID, item.ProjectID)
	if err != nil || runtime == nil || runtimeIsExpired(runtime, w.now().UTC()) {
		if err == nil {
			err = ErrRuntimeExpired
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "PROJECT_RUNTIME_EXPIRED")
		w.finish(ctx, item, models.PublicationStatusFailed, "PROJECT_RUNTIME_EXPIRED", err, nil)
		return
	}
	buildCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go w.keepAlive(buildCtx, cancel, item, done)
	result, err := w.gateway.PublishImage(buildCtx, runtime.GatewaySessionID, item.ProjectID, item.ID, item.Context, item.Dockerfile)
	cancel()
	<-done
	if err == nil && result == nil {
		err = errors.New("gateway returned an empty publication result")
	}
	if err == nil {
		span.SetAttributes(attribute.String("container.image.ref", result.ImageRef), attribute.String("container.image.digest", result.Digest))
		span.SetStatus(codes.Ok, "published")
		w.finish(context.WithoutCancel(ctx), item, models.PublicationStatusCompleted, "", nil, result)
		return
	}
	statusCtx, statusCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	cancelled, _ := w.repo.IsPublicationCancelRequested(statusCtx, item.ID)
	statusCancel()
	if cancelled {
		span.SetAttributes(attribute.String("app.publication.status", models.PublicationStatusCancelled))
		w.finish(context.WithoutCancel(ctx), item, models.PublicationStatusCancelled, "", nil, nil)
		return
	}
	code := "IMAGE_BUILD_FAILED"
	var gatewayErr *models.GatewayResponseError
	var failureResult *models.GatewayPublication
	if errors.As(err, &gatewayErr) && gatewayErr.Code != "" {
		code = gatewayErr.Code
		failureResult = &models.GatewayPublication{Logs: gatewayErr.Logs}
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, code)
	w.finish(context.WithoutCancel(ctx), item, models.PublicationStatusFailed, code, err, failureResult)
}

func (w *PublicationWorker) keepAlive(ctx context.Context, cancel context.CancelFunc, item *models.Publication, done chan<- struct{}) {
	defer close(done)
	heartbeat := time.NewTicker(w.heartbeat)
	cancelPoll := time.NewTicker(w.cancelPoll)
	defer heartbeat.Stop()
	defer cancelPoll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			ok, err := w.repo.HeartbeatPublication(ctx, item.ID, w.workerID, w.now().UTC())
			if err != nil || !ok {
				cancel()
				return
			}
		case <-cancelPoll.C:
			requested, err := w.repo.IsPublicationCancelRequested(ctx, item.ID)
			if err == nil && requested {
				cancel()
				return
			}
		}
	}
}

func (w *PublicationWorker) finish(ctx context.Context, item *models.Publication, status, code string, buildErr error, result *models.GatewayPublication) {
	message, imageRef, digest, logs := "", "", "", ""
	if buildErr != nil {
		message = publicationText(buildErr.Error())
	}
	if result != nil {
		imageRef, digest, logs = result.ImageRef, result.Digest, publicationText(result.Logs)
	}
	finishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	updated, err := w.repo.FinishPublication(finishCtx, &models.FinishPublicationInput{
		ID: item.ID, WorkerID: w.workerID, Status: status, ImageRef: imageRef, Digest: digest,
		Logs: logs, ErrorCode: code, ErrorMessage: message, Now: w.now().UTC(),
	})
	if err != nil {
		zap.L().Error("finish publication failed", zap.String("publication_id", item.ID), zap.Error(err))
	} else if !updated {
		zap.L().Warn("publication worker lease was lost before completion", zap.String("publication_id", item.ID))
	}
}

func publicationText(value string) string {
	value = strings.ReplaceAll(strings.ToValidUTF8(value, "\uFFFD"), "\x00", "\uFFFD")
	if len(value) > maxPublicationTextBytes {
		value = strings.ToValidUTF8(value[len(value)-maxPublicationTextBytes:], "\uFFFD")
	}
	return value
}

func (w *PublicationWorker) recoverOrphans(ctx context.Context) {
	now := w.now().UTC()
	count, err := w.repo.FailOrphanedPublications(ctx, now.Add(-w.orphanAge), now)
	if err != nil {
		zap.L().Warn("recover orphaned publications failed", zap.Error(err))
	} else if count != 0 {
		zap.L().Warn("failed orphaned publications", zap.Int64("count", count))
	}
}
