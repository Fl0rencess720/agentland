package biz

import (
	"context"
	"errors"
	"fmt"
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
	FailPublicationDispatch(context.Context, string, time.Time, error) error
	FindPublicationByPreparationRun(context.Context, string) (*models.Publication, error)
	PreparationRunProjected(context.Context, string, int64) (bool, error)
	PreparationSkillUsed(context.Context, string, string) (bool, error)
	CompletePublicationPreparation(context.Context, *models.CompletePublicationPreparationInput) (bool, error)
	FailPublicationPreparation(context.Context, string, string, string, string, string, time.Time) (bool, error)
	MarkPublicationDispatched(context.Context, string, time.Time) (bool, error)
	LoadPublicationSnapshot(context.Context, string) (*models.WorkspaceSnapshot, error)
	GetRuntime(context.Context, string, string) (*models.ProjectRuntime, error)
}

type PublicationWorkerRepo interface {
	ClaimPublication(context.Context, string, string, time.Time) (*models.Publication, error)
	HeartbeatPublication(context.Context, string, string, time.Time) (bool, error)
	FinishPublication(context.Context, *models.FinishPublicationInput) (bool, error)
	FailOrphanedPublications(context.Context, time.Time, time.Time) (int64, error)
	IsPublicationCancelRequested(context.Context, string) (bool, error)
	LoadPublicationSnapshot(context.Context, string) (*models.WorkspaceSnapshot, error)
}

type PublicationGateway interface {
	PublishApplication(context.Context, string, string, string, string, []byte) (*models.GatewayPublication, error)
}

const maxPublicationTextBytes = 1 << 20

var (
	errPublicationPreparationCancelled = errors.New("publication preparation was cancelled")
	ErrPublicationSnapshotInvalid      = errors.New("publication snapshot is invalid")
)

type permanentPublicationPreparationError struct{ error }

func (e *permanentPublicationPreparationError) Unwrap() error { return e.error }

func permanentPreparationError(err error) error {
	return &permanentPublicationPreparationError{error: err}
}

func (u *projectUseCase) CreatePublication(ctx context.Context, principal models.AuthPrincipal, projectID, idempotencyKey string, req *models.PublicationCreateReq) (*models.PublicationResp, *response.APIError) {
	if !authorized(principal) {
		return nil, response.UnauthorizedError()
	}
	if u.publications == nil || u.publisher == nil || u.tasks == nil {
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
		if existing.Status == models.PublicationStatusPreparing {
			if err = u.tasks.PublishRunTask(ctx, existing.PreparationRunID, existing.ProjectID); err != nil {
				return nil, mapAPIError(err)
			}
		}
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
	preparationRunID := token.NewID("run")
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	publication, existing, err := u.publications.CreatePublication(ctx, &models.CreatePublicationInput{
		ID: token.NewID("pub"), OwnerID: principal.UserID, ProjectID: projectID, IdempotencyKey: idempotencyKey,
		Context: contextPath, Dockerfile: dockerfile, PreparationRunID: preparationRunID,
		PreparationInputMessageID: token.NewID("msg"), PreparationAssistantMessageID: token.NewID("msg"),
		PreparationMessage: publicationPreparationMessage(contextPath, dockerfile),
		TraceParent:        carrier.Get("traceparent"), TraceState: carrier.Get("tracestate"), Now: now,
	})
	if err != nil {
		return nil, mapAPIError(err)
	}
	if err = u.tasks.PublishRunTask(ctx, publication.PreparationRunID, publication.ProjectID); err != nil {
		if !existing {
			_, _ = u.publications.FailPublicationPreparation(context.WithoutCancel(ctx), publication.ID, publication.PreparationRunID,
				models.PublicationStatusFailed, "PREPARATION_DISPATCH_FAILED", err.Error(), u.now().UTC())
		}
		return nil, mapAPIError(err)
	}
	_ = u.runs.TouchRuntime(ctx, projectID, now)
	return publicationResponse(publication), nil
}

func publicationPreparationMessage(buildContext, dockerfile string) string {
	return fmt.Sprintf(`Prepare this frontend project for a production container build.
You MUST first call read_skill with {"name":"dockerfile"} and follow that skill.
Inspect the project and create or repair %s and .dockerignore under build context %s.
Do not publish an image or modify application behavior. Finish only after validating the generated files.`, dockerfile, buildContext)
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
	if publication.Status == models.PublicationStatusPreparing && publication.PreparationRunID != "" {
		run, _, cancelErr := u.runs.RequestCancel(ctx, principal.UserID, publication.PreparationRunID, u.now().UTC())
		if cancelErr != nil {
			return nil, mapAPIError(cancelErr)
		}
		if run.AgentRunID != "" {
			runtime, runtimeErr := u.runs.GetRuntime(ctx, principal.UserID, publication.ProjectID)
			if runtimeErr != nil {
				return nil, mapAPIError(runtimeErr)
			}
			if runtime != nil {
				if cancelErr = u.gateway.CancelRun(ctx, runtime.GatewaySessionID, run.AgentRunID); cancelErr != nil {
					var gatewayErr *models.GatewayResponseError
					if !errors.As(cancelErr, &gatewayErr) || gatewayErr.StatusCode != 404 {
						return nil, gatewayAPIError(cancelErr)
					}
				}
			}
		}
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
		PreparationRunID: item.PreparationRunID,
		ImageRef:         item.ImageRef, Digest: item.Digest, DeploymentURL: item.DeploymentURL,
		DeploymentHostname: item.DeploymentHostname, DeploymentName: item.DeploymentName,
		Logs: item.Logs, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage,
		CancelRequestedAt: optionalTime(item.CancelRequestedAt), CreatedAt: formatTime(item.CreatedAt),
		StartedAt: optionalTime(item.StartedAt), CompletedAt: optionalTime(item.CompletedAt),
	}
}

type PublicationPreparationCoordinator struct {
	repo    PublicationRepo
	gateway AgentlandGateway
	tasks   TaskPublisher
	now     func() time.Time
}

func NewPublicationPreparationCoordinator(repo PublicationRepo, gateway AgentlandGateway, tasks TaskPublisher) *PublicationPreparationCoordinator {
	return &PublicationPreparationCoordinator{repo: repo, gateway: gateway, tasks: tasks, now: time.Now}
}

func (c *PublicationPreparationCoordinator) Handle(ctx context.Context, event *models.AgentEvent) error {
	if event == nil || !isTerminalEvent(event.Type) {
		return nil
	}
	item, err := c.repo.FindPublicationByPreparationRun(ctx, event.RunID)
	if err != nil || item == nil {
		return err
	}
	if item.Status == models.PublicationStatusQueued {
		return c.dispatch(ctx, item)
	}
	if item.Status != models.PublicationStatusPreparing {
		return nil
	}
	projected, err := c.repo.PreparationRunProjected(ctx, event.RunID, event.Sequence)
	if err != nil {
		return err
	}
	if !projected {
		return errors.New("publication preparation run is not projected yet")
	}
	now := c.now().UTC()
	if event.Type == "run.cancelled" || item.CancelRequestedAt != nil {
		_, err = c.repo.FailPublicationPreparation(ctx, item.ID, item.PreparationRunID, models.PublicationStatusCancelled, "", "", now)
		return err
	}
	if event.Type == "run.failed" {
		_, err = c.repo.FailPublicationPreparation(ctx, item.ID, item.PreparationRunID, models.PublicationStatusFailed,
			"DOCKERFILE_PREPARATION_FAILED", eventError(event), now)
		return err
	}
	if err = c.complete(ctx, item, now); err != nil {
		if errors.Is(err, errPublicationPreparationCancelled) {
			_, cancelErr := c.repo.FailPublicationPreparation(context.WithoutCancel(ctx), item.ID, item.PreparationRunID,
				models.PublicationStatusCancelled, "", "", now)
			return cancelErr
		}
		var permanent *permanentPublicationPreparationError
		if !errors.As(err, &permanent) && !errors.Is(err, ErrPublicationSnapshotInvalid) {
			return err
		}
		_, failErr := c.repo.FailPublicationPreparation(context.WithoutCancel(ctx), item.ID, item.PreparationRunID,
			models.PublicationStatusFailed, "DOCKERFILE_PREPARATION_FAILED", err.Error(), now)
		return failErr
	}
	item, err = c.repo.FindPublicationByPreparationRun(ctx, event.RunID)
	if err != nil || item == nil {
		return err
	}
	return c.dispatch(ctx, item)
}

func (c *PublicationPreparationCoordinator) complete(ctx context.Context, item *models.Publication, now time.Time) error {
	used, err := c.repo.PreparationSkillUsed(ctx, item.PreparationRunID, "dockerfile")
	if err != nil {
		return err
	}
	if !used {
		return permanentPreparationError(errors.New("preparation agent did not read the dockerfile skill"))
	}
	runtime, err := c.repo.GetRuntime(ctx, item.OwnerID, item.ProjectID)
	if err != nil {
		return err
	}
	if runtime == nil || runtimeIsExpired(runtime, now) {
		return permanentPreparationError(ErrRuntimeExpired)
	}
	dockerfilePath := path.Join(item.Context, item.Dockerfile)
	file, err := c.gateway.GetFile(ctx, runtime.GatewaySessionID, dockerfilePath)
	if err != nil {
		return classifyPreparationGatewayError("validate Dockerfile", err)
	}
	if file == nil || file.Path == "" || file.Size <= 0 {
		return permanentPreparationError(errors.New("Dockerfile is missing or is not a regular file"))
	}
	if err = validateRuntimeDockerfile(file.Content); err != nil {
		return permanentPreparationError(err)
	}
	dockerignorePath := path.Join(item.Context, ".dockerignore")
	dockerignore, err := c.gateway.GetFile(ctx, runtime.GatewaySessionID, dockerignorePath)
	if err != nil {
		return classifyPreparationGatewayError("validate .dockerignore", err)
	}
	if dockerignore == nil || dockerignore.Path == "" || dockerignore.Size <= 0 {
		return permanentPreparationError(errors.New(".dockerignore is missing or is not a regular file"))
	}
	replayGateway, ok := c.gateway.(ReplayGateway)
	if !ok {
		return permanentPreparationError(errors.New("gateway does not support workspace snapshots"))
	}
	snapshot, err := replayGateway.GetWorkspaceSnapshot(ctx, runtime.GatewaySessionID)
	if err != nil {
		return classifyPreparationGatewayError("capture prepared workspace", err)
	}
	transitioned, err := c.repo.CompletePublicationPreparation(ctx, &models.CompletePublicationPreparationInput{
		ID: item.ID, PreparationRunID: item.PreparationRunID, Snapshot: snapshot,
		Now: now,
	})
	if err != nil {
		return err
	}
	if !transitioned {
		latest, loadErr := c.repo.FindPublicationByPreparationRun(ctx, item.PreparationRunID)
		if loadErr != nil {
			return loadErr
		}
		if latest == nil || latest.Status != models.PublicationStatusQueued {
			if latest != nil && latest.Status == models.PublicationStatusPreparing && latest.CancelRequestedAt != nil {
				return errPublicationPreparationCancelled
			}
			return errors.New("publication preparation state changed")
		}
	}
	return nil
}

func validateRuntimeDockerfile(content string) error {
	var exposed, nonRootUser bool
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			exposed, nonRootUser = false, false
		case "EXPOSE":
			for _, value := range fields[1:] {
				if strings.TrimSuffix(value, "/tcp") == "8080" {
					exposed = true
				}
			}
		case "USER":
			nonRootUser = false
			if len(fields) > 1 {
				user := strings.SplitN(fields[1], ":", 2)[0]
				nonRootUser = user != "" && user != "0" && strings.IndexFunc(user, func(r rune) bool { return r < '0' || r > '9' }) == -1
			}
		}
	}
	if !exposed {
		return errors.New("final Dockerfile stage must expose port 8080")
	}
	if !nonRootUser {
		return errors.New("final Dockerfile stage must declare a numeric non-root USER")
	}
	return nil
}

func classifyPreparationGatewayError(operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	var gatewayErr *models.GatewayResponseError
	if errors.As(err, &gatewayErr) && gatewayErr.StatusCode >= 400 && gatewayErr.StatusCode < 500 && gatewayErr.StatusCode != 408 && gatewayErr.StatusCode != 429 {
		return permanentPreparationError(wrapped)
	}
	return wrapped
}

func (c *PublicationPreparationCoordinator) dispatch(ctx context.Context, item *models.Publication) error {
	if item.Status != models.PublicationStatusQueued || item.BuildDispatchedAt != nil {
		return nil
	}
	if c.tasks == nil {
		return errors.New("publication task publisher is not configured")
	}
	if err := c.tasks.PublishPublicationTask(ctx, item.ID, item.ProjectID); err != nil {
		_ = c.repo.FailPublicationDispatch(context.WithoutCancel(ctx), item.ID, c.now().UTC(), err)
		return nil
	}
	_, err := c.repo.MarkPublicationDispatched(ctx, item.ID, c.now().UTC())
	return err
}

type PublicationWorker struct {
	repo       PublicationWorkerRepo
	gateway    PublicationGateway
	queue      TaskQueue
	workerID   string
	now        func() time.Time
	heartbeat  time.Duration
	cancelPoll time.Duration
	orphanAge  time.Duration
	parallel   int
}

func NewPublicationWorker(repo PublicationWorkerRepo, gateway PublicationGateway, queues ...TaskQueue) *PublicationWorker {
	worker := &PublicationWorker{
		repo: repo, gateway: gateway, workerID: token.NewID("publisher"), now: time.Now,
		heartbeat:  configDuration("publication.worker.heartbeat_interval", 5*time.Second),
		cancelPoll: configDuration("publication.worker.cancel_poll_interval", 250*time.Millisecond),
		orphanAge:  configDuration("publication.worker.orphan_timeout", 30*time.Second),
		parallel:   configInt("publication.worker.parallelism", 2),
	}
	if len(queues) != 0 {
		worker.queue = queues[0]
	}
	return worker
}

func (w *PublicationWorker) Run(ctx context.Context) {
	if w.queue == nil {
		zap.L().Error("publication task queue is not configured")
		return
	}
	w.recoverOrphans(ctx)
	go func() {
		sweep := time.NewTicker(w.orphanAge)
		defer sweep.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sweep.C:
				w.recoverOrphans(ctx)
			}
		}
	}()
	semaphore := make(chan struct{}, w.parallel)
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			return
		}
		delivery, err := w.queue.Receive(ctx)
		if err != nil {
			<-semaphore
			if ctx.Err() == nil {
				zap.L().Warn("receive publication failed", zap.Error(err))
				waitWorkerRetry(ctx, time.Second)
			}
			continue
		}
		var publication *models.Publication
		for ctx.Err() == nil {
			publication, err = w.repo.ClaimPublication(ctx, delivery.ID(), w.workerID, w.now().UTC())
			if err != nil {
				zap.L().Warn("claim publication failed", zap.String("publication_id", delivery.ID()), zap.Error(err))
				waitWorkerRetry(ctx, 250*time.Millisecond)
				continue
			}
			break
		}
		for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
			if err = delivery.Ack(ctx); err == nil {
				break
			}
			zap.L().Warn("commit publication delivery failed", zap.String("publication_id", delivery.ID()), zap.Error(err))
			waitWorkerRetry(ctx, 250*time.Millisecond)
		}
		if ctx.Err() != nil {
			<-semaphore
			return
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

func (w *PublicationWorker) execute(parent context.Context, item *models.Publication) {
	if item.TraceParent != "" {
		carrier := propagation.MapCarrier{"traceparent": item.TraceParent, "tracestate": item.TraceState}
		parent = propagation.TraceContext{}.Extract(parent, carrier)
	}
	ctx, span := otel.Tracer("agentland/app-be/publisher").Start(parent, "publication.execute", trace.WithAttributes(
		attribute.String("app.publication.id", item.ID), attribute.String("app.project.id", item.ProjectID),
	))
	defer span.End()
	snapshot, err := w.repo.LoadPublicationSnapshot(ctx, item.ID)
	if err != nil || snapshot == nil || len(snapshot.Data) == 0 {
		if err == nil {
			err = errors.New("publication snapshot is missing")
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "PUBLICATION_SNAPSHOT_INVALID")
		w.finish(ctx, item, models.PublicationStatusFailed, "PUBLICATION_SNAPSHOT_INVALID", err, nil)
		return
	}
	buildCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go w.keepAlive(buildCtx, cancel, item, done)
	result, err := w.gateway.PublishApplication(buildCtx, item.ProjectID, item.ID, item.Context, item.Dockerfile, snapshot.Data)
	cancel()
	<-done
	if err == nil && result == nil {
		err = errors.New("gateway returned an empty publication result")
	}
	if err == nil {
		span.SetAttributes(attribute.String("container.image.ref", result.ImageRef), attribute.String("container.image.digest", result.Digest), attribute.String("application.deployment.url", result.DeploymentURL))
		span.SetStatus(codes.Ok, "deployed")
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
		failureResult = &models.GatewayPublication{ImageRef: gatewayErr.ImageRef, Digest: gatewayErr.Digest, DeploymentURL: gatewayErr.DeploymentURL, Logs: gatewayErr.Logs}
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
			if err != nil {
				zap.L().Warn("renew publication worker lease failed", zap.String("publication_id", item.ID), zap.Error(err))
			} else if !ok {
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
	message, imageRef, digest, deploymentURL, deploymentHostname, deploymentName, logs := "", "", "", "", "", "", ""
	if buildErr != nil {
		message = publicationText(buildErr.Error())
	}
	if result != nil {
		imageRef, digest, deploymentURL = result.ImageRef, result.Digest, result.DeploymentURL
		deploymentHostname, deploymentName, logs = result.DeploymentHostname, result.DeploymentName, publicationText(result.Logs)
	}
	finishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	updated, err := w.repo.FinishPublication(finishCtx, &models.FinishPublicationInput{
		ID: item.ID, WorkerID: w.workerID, Status: status, ImageRef: imageRef, Digest: digest,
		DeploymentURL: deploymentURL, DeploymentHostname: deploymentHostname, DeploymentName: deploymentName,
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
