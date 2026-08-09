package biz

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"github.com/spf13/viper"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type RunWorker struct {
	repo       RunWorkerRepo
	gateway    AgentlandGateway
	queue      TaskQueue
	events     RunEventPublisher
	workerID   string
	now        func() time.Time
	renewEvery time.Duration
	runtimeMax time.Duration
	parallel   int
	slots      chan struct{}
	wg         sync.WaitGroup
}

func NewRunWorker(repo RunWorkerRepo, gateway AgentlandGateway, queues ...TaskQueue) *RunWorker {
	worker := &RunWorker{
		repo: repo, gateway: gateway, workerID: token.NewID("worker"), now: time.Now,
		renewEvery: configDuration("worker.heartbeat_interval", 2*time.Second),
		runtimeMax: configDuration("runtime.max_session_duration", time.Hour),
		parallel:   configInt("worker.parallelism", 4),
	}
	if len(queues) != 0 {
		worker.queue = queues[0]
	}
	if publisher, ok := any(repo).(RunEventPublisher); ok {
		worker.events = publisher
	}
	worker.slots = make(chan struct{}, worker.parallel)
	return worker
}

func (w *RunWorker) execute(ctx context.Context, run *models.Run) {
	if requested, _ := w.repo.IsCancelRequested(ctx, run.ID); requested {
		_ = w.publishCancellation(ctx, run)
		return
	}
	runtime, err := w.startAgentRun(ctx, run, w.workerID)
	if err != nil {
		if errors.Is(err, ErrRunLeaseLost) {
			return
		}
		if errors.Is(err, ErrRunCancelled) {
			_ = w.publishCancellation(ctx, run)
			return
		}
		_ = w.publishFailure(ctx, run, runStartErrorCode(err), err)
		return
	}
	if requested, _ := w.repo.IsCancelRequested(ctx, run.ID); requested {
		_ = w.gateway.CancelRun(ctx, runtime.GatewaySessionID, run.ID)
	}
	w.pump(ctx, run, runtime, w.workerID)
}

func NewRunWorkerWithEvents(repo RunWorkerRepo, gateway AgentlandGateway, queue TaskQueue, events RunEventPublisher) *RunWorker {
	worker := NewRunWorker(repo, gateway, queue)
	worker.events = events
	return worker
}

func (w *RunWorker) Run(ctx context.Context) {
	if w.queue == nil || w.events == nil {
		zap.L().Error("run worker dependencies are not configured")
		return
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.watchExpired(ctx)
	}()
	defer w.wg.Wait()
	for {
		select {
		case w.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		delivery, err := w.queue.Receive(ctx)
		if err != nil {
			<-w.slots
			if ctx.Err() == nil {
				zap.L().Warn("receive app run failed", zap.Error(err))
				waitWorkerRetry(ctx, time.Second)
			}
			continue
		}
		run, err := w.repo.GetRunForExecution(ctx, delivery.ID())
		if err != nil {
			<-w.slots
			zap.L().Warn("load app run failed", zap.String("run_id", delivery.ID()), zap.Error(err))
			waitWorkerRetry(ctx, 250*time.Millisecond)
			continue
		}
		if run == nil || isTerminalStatus(run.Status) {
			_ = delivery.Ack(ctx)
			<-w.slots
			continue
		}
		owned, err := w.repo.AcquireRunOwnership(ctx, run.ID, w.workerID)
		if err != nil {
			if publishErr := w.publishFailure(ctx, run, "WORKER_OWNERSHIP_FAILED", err); publishErr == nil {
				_ = delivery.Ack(ctx)
			}
			<-w.slots
			continue
		}
		if !owned {
			_ = delivery.Ack(ctx)
			<-w.slots
			continue
		}
		requested, requestErr := w.repo.IsCancelRequested(ctx, run.ID)
		if requestErr != nil {
			_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), run.ID, w.workerID)
			<-w.slots
			waitWorkerRetry(ctx, 250*time.Millisecond)
			continue
		}
		if requested {
			if publishErr := w.publishCancellation(ctx, run); publishErr == nil {
				_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), run.ID, w.workerID)
				_ = delivery.Ack(ctx)
			}
			<-w.slots
			continue
		}
		runtime, err := w.startAgentRun(ctx, run, w.workerID)
		if err != nil {
			if errors.Is(err, ErrRunLeaseLost) {
				_ = delivery.Ack(ctx)
				<-w.slots
				continue
			}
			var publishErr error
			if errors.Is(err, ErrRunCancelled) {
				publishErr = w.publishCancellation(ctx, run)
			} else {
				publishErr = w.publishFailure(ctx, run, runStartErrorCode(err), err)
			}
			if publishErr == nil {
				_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), run.ID, w.workerID)
				_ = delivery.Ack(ctx)
			}
			<-w.slots
			continue
		}
		if requested, requestErr := w.repo.IsCancelRequested(ctx, run.ID); requestErr == nil && requested {
			if cancelErr := w.gateway.CancelRun(ctx, runtime.GatewaySessionID, run.ID); cancelErr != nil {
				zap.L().Warn("cancel newly started agent run failed", zap.String("run_id", run.ID), zap.Error(cancelErr))
			}
		}
		if err = delivery.Ack(ctx); err != nil {
			zap.L().Warn("commit app run delivery failed", zap.String("run_id", run.ID), zap.Error(err))
		}
		w.startPump(ctx, run, runtime, w.workerID)
	}
}

func (w *RunWorker) startAgentRun(ctx context.Context, run *models.Run, owner string) (*models.ProjectRuntime, error) {
	async, ok := w.gateway.(AsyncAgentlandGateway)
	if !ok {
		return nil, errors.New("gateway does not support asynchronous agent runs")
	}
	runtime, err := w.repo.GetRuntime(ctx, run.OwnerID, run.ProjectID)
	if err != nil {
		return nil, err
	}
	now := w.now().UTC()
	if runtime != nil && runtimeIsExpired(runtime, now) {
		_ = w.repo.ExpireRuntime(ctx, run.OwnerID, run.ProjectID, now)
		return nil, ErrRuntimeExpired
	}
	seedSession := ""
	if runtime != nil {
		seedSession = runtime.GatewaySessionID
	}
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	lost := make(chan struct{})
	stopRenew := make(chan struct{})
	defer close(stopRenew)
	go w.renewOwnership(readyCtx, run.ID, owner, stopRenew, lost)
	go func() {
		select {
		case <-lost:
			cancel()
		case <-readyCtx.Done():
		case <-stopRenew:
		}
	}()
	sessionID, err := w.gateway.EnsureRuntime(readyCtx, seedSession)
	if ownershipWasLost(lost) {
		return nil, ErrRunLeaseLost
	}
	if err != nil {
		return nil, err
	}
	if runtime == nil {
		runtime = &models.ProjectRuntime{ProjectID: run.ProjectID, OwnerID: run.OwnerID, GatewaySessionID: sessionID, AgentConversationID: run.ProjectID, Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(w.runtimeMax), UpdatedAt: now}
	} else {
		runtime.GatewaySessionID, runtime.Status, runtime.LastActiveAt, runtime.UpdatedAt = sessionID, models.RuntimeStatusActive, now, now
	}
	if err = w.repo.UpsertRuntime(readyCtx, runtime); err != nil {
		if ownershipWasLost(lost) {
			return nil, ErrRunLeaseLost
		}
		return nil, err
	}
	owned, err := w.repo.RenewRunOwnership(readyCtx, run.ID, owner)
	if err != nil || !owned {
		return nil, ErrRunLeaseLost
	}
	if err = w.captureWorkspaceSnapshot(readyCtx, run, sessionID); err != nil {
		if ownershipWasLost(lost) {
			return nil, ErrRunLeaseLost
		}
		return nil, err
	}
	if ownershipWasLost(lost) {
		return nil, ErrRunLeaseLost
	}
	if requested, requestErr := w.repo.IsCancelRequested(readyCtx, run.ID); requestErr != nil {
		if ownershipWasLost(lost) {
			return nil, ErrRunLeaseLost
		}
		return nil, requestErr
	} else if requested {
		return nil, ErrRunCancelled
	}
	state, err := async.StartAgentRun(readyCtx, sessionID, run.ID, runtime.AgentConversationID, run.InputMessage)
	if err != nil {
		if ownershipWasLost(lost) {
			return nil, ErrRunLeaseLost
		}
		return nil, err
	}
	if state == nil || state.RunID != run.ID {
		return nil, errors.New("agentd returned an invalid run")
	}
	if ownershipWasLost(lost) {
		return nil, ErrRunLeaseLost
	}
	return runtime, nil
}

func ownershipWasLost(lost <-chan struct{}) bool {
	select {
	case <-lost:
		return true
	default:
		return false
	}
}

func (w *RunWorker) startPump(parent context.Context, run *models.Run, runtime *models.ProjectRuntime, owner string) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() { <-w.slots }()
		w.pump(parent, run, runtime, owner)
	}()
}

func (w *RunWorker) pump(parent context.Context, run *models.Run, runtime *models.ProjectRuntime, owner string) {
	async, ok := w.gateway.(AsyncAgentlandGateway)
	if !ok {
		return
	}
	if run.TraceParent != "" {
		carrier := propagation.MapCarrier{"traceparent": run.TraceParent, "tracestate": run.TraceState}
		parent = propagation.TraceContext{}.Extract(parent, carrier)
	}
	ctx, span := otel.Tracer("agentland/app-be/worker").Start(parent, "run.forward_events", trace.WithAttributes(
		attribute.String("app.run.id", run.ID), attribute.String("app.project.id", run.ProjectID), attribute.String("app.worker.id", owner),
	))
	defer span.End()
	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lost := make(chan struct{})
	stopRenew := make(chan struct{})
	defer close(stopRenew)
	go w.renewOwnership(pumpCtx, run.ID, owner, stopRenew, lost)
	go func() {
		select {
		case <-lost:
			cancel()
		case <-pumpCtx.Done():
		case <-stopRenew:
		}
	}()
	after := run.LastSequence
	for pumpCtx.Err() == nil {
		terminal := false
		err := async.StreamAgentRun(pumpCtx, runtime.GatewaySessionID, run.ID, after, func(event *models.AgentEvent) error {
			event.RunID = run.ID
			if event.ConversationID == "" {
				event.ConversationID = run.ProjectID
			}
			if event.Timestamp.IsZero() {
				event.Timestamp = w.now().UTC()
			}
			if err := w.events.PublishRunEvent(pumpCtx, event); err != nil {
				return err
			}
			if event.Sequence > after {
				after = event.Sequence
			}
			terminal = isTerminalEvent(event.Type)
			return nil
		})
		if terminal {
			_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(parent), run.ID, owner)
			span.SetStatus(codes.Ok, "run finished")
			return
		}
		select {
		case <-lost:
			span.SetAttributes(attribute.Bool("app.run.ownership_lost", true))
			return
		default:
		}
		if pumpCtx.Err() != nil {
			return
		}
		if err != nil {
			zap.L().Warn("agent event stream interrupted", zap.String("run_id", run.ID), zap.Error(err))
		}
		waitWorkerRetry(pumpCtx, 250*time.Millisecond)
	}
}

func (w *RunWorker) renewOwnership(ctx context.Context, runID, owner string, done <-chan struct{}, lost chan<- struct{}) {
	ticker := time.NewTicker(w.renewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			owned, err := w.repo.RenewRunOwnership(ctx, runID, owner)
			if err != nil || !owned {
				if err != nil {
					zap.L().Warn("renew run ownership failed", zap.String("run_id", runID), zap.Error(err))
				}
				close(lost)
				return
			}
		}
	}
}

func (w *RunWorker) watchExpired(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.recoverExpired(ctx)
		}
	}
}

func (w *RunWorker) recoverExpired(ctx context.Context) {
	candidates, err := w.repo.ExpiredRunOwnerships(ctx, w.now().UTC(), 100)
	if err != nil {
		zap.L().Warn("list expired run ownerships failed", zap.Error(err))
		return
	}
	for _, candidate := range candidates {
		select {
		case w.slots <- struct{}{}:
		default:
			return
		}
		owner := w.workerID + ":recovery"
		acquired, takeErr := w.repo.TakeoverRunOwnership(ctx, candidate.ID, candidate.OwnerID, owner)
		if takeErr != nil || !acquired {
			<-w.slots
			continue
		}
		run, loadErr := w.repo.GetRunForExecution(ctx, candidate.ID)
		if loadErr != nil {
			<-w.slots
			continue
		}
		if run == nil || isTerminalStatus(run.Status) {
			_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), candidate.ID, owner)
			<-w.slots
			continue
		}
		runtime, runtimeErr := w.repo.GetRuntime(ctx, run.OwnerID, run.ProjectID)
		if runtimeErr != nil {
			<-w.slots
			continue
		}
		if runtime == nil {
			runtime, runtimeErr = w.startAgentRun(ctx, run, owner)
			if runtimeErr != nil {
				if errors.Is(runtimeErr, ErrRunLeaseLost) {
					<-w.slots
					continue
				}
				var publishErr error
				if errors.Is(runtimeErr, ErrRunCancelled) {
					publishErr = w.publishCancellation(ctx, run)
				} else {
					publishErr = w.publishFailure(ctx, run, "AGENT_RUN_RECOVERY_FAILED", runtimeErr)
				}
				if publishErr == nil {
					_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), run.ID, owner)
				}
				<-w.slots
				continue
			}
			w.startPump(ctx, run, runtime, owner)
			continue
		}
		if runtimeIsExpired(runtime, w.now().UTC()) {
			if publishErr := w.publishFailure(ctx, run, "PROJECT_RUNTIME_EXPIRED", ErrRuntimeExpired); publishErr == nil {
				_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), run.ID, owner)
			}
			<-w.slots
			continue
		}
		if async, ok := w.gateway.(AsyncAgentlandGateway); ok {
			if _, statusErr := async.GetAgentRun(ctx, runtime.GatewaySessionID, run.ID); statusErr != nil {
				var gatewayErr *models.GatewayResponseError
				if errors.As(statusErr, &gatewayErr) && gatewayErr.StatusCode == 404 {
					if _, startErr := async.StartAgentRun(ctx, runtime.GatewaySessionID, run.ID, runtime.AgentConversationID, run.InputMessage); startErr != nil {
						if publishErr := w.publishFailure(ctx, run, "AGENT_RUN_RECOVERY_FAILED", startErr); publishErr == nil {
							_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), run.ID, owner)
						}
						<-w.slots
						continue
					}
				}
			}
		} else {
			if publishErr := w.publishFailure(ctx, run, "AGENT_RUN_RECOVERY_FAILED", errors.New("gateway does not support asynchronous agent runs")); publishErr == nil {
				_, _ = w.repo.ReleaseRunOwnership(context.WithoutCancel(ctx), run.ID, owner)
			}
			<-w.slots
			continue
		}
		w.startPump(ctx, run, runtime, owner)
	}
}

func (w *RunWorker) captureWorkspaceSnapshot(ctx context.Context, run *models.Run, sessionID string) error {
	artifacts, repoOK := w.repo.(RunArtifactRepo)
	replayGateway, gatewayOK := w.gateway.(ReplayGateway)
	if !repoOK || !gatewayOK {
		return nil
	}
	snapshot, captureErr := replayGateway.GetWorkspaceSnapshot(ctx, sessionID)
	captureError, sha := "", ""
	if captureErr != nil {
		captureError, snapshot = captureErr.Error(), []byte{}
	} else {
		digest := sha256.Sum256(snapshot)
		sha = fmt.Sprintf("%x", digest[:])
	}
	acquired, err := artifacts.SaveWorkspaceSnapshot(ctx, run.ID, snapshot, sha, captureError, w.now().UTC())
	if err != nil {
		return err
	}
	if !acquired {
		return ErrRunLeaseLost
	}
	return nil
}

func (w *RunWorker) publishFailure(ctx context.Context, run *models.Run, code string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	payload, _ := json.Marshal(map[string]string{"code": code, "error": message})
	event := &models.AgentEvent{
		Type: "run.failed", RunID: run.ID, ConversationID: run.ProjectID, Sequence: run.LastSequence + 1,
		Timestamp: w.now().UTC(), Payload: payload,
	}
	if w.events == nil {
		return errors.New("run event publisher is not configured")
	}
	if err := w.events.PublishRunEvent(ctx, event); err != nil {
		zap.L().Error("publish run failure failed", zap.String("run_id", run.ID), zap.Error(err))
		return err
	}
	return nil
}

func (w *RunWorker) publishCancellation(ctx context.Context, run *models.Run) error {
	event := &models.AgentEvent{
		Type: "run.cancelled", RunID: run.ID, ConversationID: run.ProjectID,
		Sequence: run.LastSequence + 1, Timestamp: w.now().UTC(), Payload: json.RawMessage(`{}`),
	}
	if w.events == nil {
		return errors.New("run event publisher is not configured")
	}
	if err := w.events.PublishRunEvent(ctx, event); err != nil {
		zap.L().Error("publish run cancellation failed", zap.String("run_id", run.ID), zap.Error(err))
		return err
	}
	return nil
}

func runStartErrorCode(err error) string {
	if errors.Is(err, ErrRuntimeExpired) {
		return "PROJECT_RUNTIME_EXPIRED"
	}
	var gatewayErr *models.GatewayResponseError
	if errors.As(err, &gatewayErr) && gatewayErr.Code != "" {
		return gatewayErr.Code
	}
	return "AGENT_RUN_START_FAILED"
}

func configDuration(key string, fallback time.Duration) time.Duration {
	value := viper.GetDuration(key)
	if value <= 0 {
		return fallback
	}
	return value
}

func configInt(key string, fallback int) int {
	value := viper.GetInt(key)
	if value <= 0 {
		return fallback
	}
	return value
}

func waitWorkerRetry(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func eventError(event *models.AgentEvent) string {
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(event.Payload, &payload) == nil {
		if strings.TrimSpace(payload.Error) != "" {
			return strings.TrimSpace(payload.Error)
		}
		if strings.TrimSpace(payload.Message) != "" {
			return strings.TrimSpace(payload.Message)
		}
	}
	return "agent run failed"
}
