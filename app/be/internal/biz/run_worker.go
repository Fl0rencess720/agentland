package biz

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

const (
	terminalEventTTL              = 24 * time.Hour
	terminalEventOperationTimeout = 2 * time.Second
	assistantDeltaFlushInterval   = 250 * time.Millisecond
)

type RunWorker struct {
	repo       RunWorkerRepo
	events     RunEventStore
	gateway    AgentlandGateway
	workerID   string
	now        func() time.Time
	poll       time.Duration
	heartbeat  time.Duration
	cancelPoll time.Duration
	orphanAge  time.Duration
	runtimeMax time.Duration
	parallel   int
}

func NewRunWorker(repo RunWorkerRepo, events RunEventStore, gateway AgentlandGateway) *RunWorker {
	return &RunWorker{
		repo: repo, events: events, gateway: gateway, workerID: token.NewID("worker"), now: time.Now,
		poll:       configDuration("worker.poll_interval", 500*time.Millisecond),
		heartbeat:  configDuration("worker.heartbeat_interval", 5*time.Second),
		cancelPoll: configDuration("worker.cancel_poll_interval", 250*time.Millisecond),
		orphanAge:  configDuration("worker.orphan_timeout", 30*time.Second),
		runtimeMax: configDuration("runtime.max_session_duration", time.Hour),
		parallel:   configInt("worker.parallelism", 4),
	}
}

func (w *RunWorker) Run(ctx context.Context) {
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
			run, err := w.repo.ClaimNextRun(ctx, w.workerID, w.now().UTC())
			if err != nil {
				<-semaphore
				zap.L().Warn("claim queued app run failed", zap.Error(err))
				continue
			}
			if run == nil {
				<-semaphore
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-semaphore }()
				w.execute(ctx, run)
			}()
		}
	}
}

func (w *RunWorker) execute(parent context.Context, run *models.Run) {
	if run.TraceParent != "" {
		carrier := propagation.MapCarrier{"traceparent": run.TraceParent}
		if run.TraceState != "" {
			carrier.Set("tracestate", run.TraceState)
		}
		parent = propagation.TraceContext{}.Extract(parent, carrier)
	}
	ctx, span := otel.Tracer("agentland/app-be/worker").Start(parent, "run.execute", trace.WithAttributes(
		attribute.String("app.run.id", run.ID),
		attribute.String("app.project.id", run.ProjectID),
		attribute.String("app.worker.id", w.workerID),
		attribute.Int64("app.run.queue_ms", max(0, w.now().UTC().Sub(run.CreatedAt).Milliseconds())),
	))
	defer span.End()
	parent = ctx
	if w.finishCancellation(parent, run) {
		return
	}

	runtime, err := w.repo.GetRuntime(parent, run.OwnerID, run.ProjectID)
	if err != nil {
		w.fail(parent, run, "RUNTIME_LOOKUP_FAILED", err)
		return
	}
	now := w.now().UTC()
	if runtime != nil && runtimeIsExpired(runtime, now) {
		_ = w.repo.ExpireRuntime(parent, run.OwnerID, run.ProjectID, now)
		w.fail(parent, run, "PROJECT_RUNTIME_EXPIRED", ErrRuntimeExpired)
		return
	}
	seedSession := ""
	if runtime != nil {
		seedSession = runtime.GatewaySessionID
	}
	readyCtx, readyCancel := context.WithTimeout(parent, 2*time.Minute)
	readyDone := make(chan struct{})
	readyStopped := make(chan struct{})
	preparationLeaseLost := atomic.Bool{}
	go func() {
		defer close(readyStopped)
		w.keepRuntimePreparationAlive(readyCtx, readyCancel, run, &preparationLeaseLost, readyDone)
	}()
	sessionID, err := w.gateway.EnsureRuntime(readyCtx, seedSession)
	close(readyDone)
	readyCancel()
	<-readyStopped
	if w.finishCancellation(parent, run) {
		return
	}
	if preparationLeaseLost.Load() {
		return
	}
	if err != nil {
		var gatewayErr *models.GatewayResponseError
		if errors.As(err, &gatewayErr) && gatewayErr.Code == "PROJECT_RUNTIME_EXPIRED" {
			_ = w.repo.ExpireRuntime(parent, run.OwnerID, run.ProjectID, w.now().UTC())
			w.fail(parent, run, "PROJECT_RUNTIME_EXPIRED", err)
			return
		}
		w.fail(parent, run, "PROJECT_RUNTIME_UNAVAILABLE", err)
		return
	}
	if !w.confirmLease(parent, run) {
		return
	}
	if runtime == nil {
		runtime = &models.ProjectRuntime{ProjectID: run.ProjectID, OwnerID: run.OwnerID, GatewaySessionID: sessionID, AgentConversationID: run.ProjectID, Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(w.runtimeMax), UpdatedAt: now}
	} else {
		runtime.GatewaySessionID, runtime.Status, runtime.LastActiveAt, runtime.UpdatedAt = sessionID, models.RuntimeStatusActive, now, now
	}
	if err = w.repo.UpsertRuntime(parent, runtime); err != nil {
		w.fail(parent, run, "RUNTIME_PERSIST_FAILED", err)
		return
	}
	if w.finishCancellation(parent, run) {
		return
	}
	if !w.confirmLease(parent, run) {
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	var agentRunID atomic.Value
	agentRunID.Store("")
	terminal := atomic.Bool{}
	runtimeLost := atomic.Bool{}
	heartbeatDone := make(chan struct{})
	go w.keepAlive(runCtx, cancel, run, runtime, &agentRunID, &runtimeLost, heartbeatDone)
	var pendingDelta strings.Builder
	pendingEvents := make([]*models.AgentEvent, 0, 32)
	var pendingSequence int64
	var pendingMu sync.Mutex
	flushDelta := func(ctx context.Context) error {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		if pendingDelta.Len() == 0 && len(pendingEvents) == 0 {
			return nil
		}
		if pendingDelta.Len() != 0 {
			delta := pendingDelta.String()
			acquired, err := w.repo.AppendAssistantDelta(ctx, run.ID, w.workerID, delta, pendingSequence, w.now().UTC())
			if err != nil {
				return err
			}
			if !acquired {
				return ErrRunLeaseLost
			}
			pendingDelta.Reset()
		}
		for len(pendingEvents) != 0 {
			if _, err := w.events.Publish(ctx, run.ID, pendingEvents[0]); err != nil {
				return err
			}
			pendingEvents = pendingEvents[1:]
		}
		return nil
	}
	flushLoopCtx, stopFlushLoop := context.WithCancel(runCtx)
	flushLoopDone := make(chan struct{})
	periodicFlushErr := make(chan error, 1)
	go func() {
		defer close(flushLoopDone)
		ticker := time.NewTicker(assistantDeltaFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-flushLoopCtx.Done():
				return
			case <-ticker.C:
				if err := flushDelta(flushLoopCtx); err != nil {
					periodicFlushErr <- err
					cancel()
					return
				}
			}
		}
	}()
	err = w.gateway.StreamChat(runCtx, runtime.GatewaySessionID, runtime.AgentConversationID, run.InputMessage, func(event *models.AgentEvent) error {
		upstreamRunID := strings.TrimSpace(event.RunID)
		if event.Type == "run.started" && upstreamRunID != "" {
			agentRunID.Store(upstreamRunID)
		}
		event.RunID = run.ID
		if event.Sequence > run.LastSequence {
			run.LastSequence = event.Sequence
		}
		if event.Timestamp.IsZero() {
			event.Timestamp = w.now().UTC()
		}
		if event.Type == "message.delta" {
			var payload struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil && payload.Content != "" {
				pendingMu.Lock()
				pendingDelta.WriteString(payload.Content)
				pendingEvents = append(pendingEvents, event)
				pendingSequence = event.Sequence
				shouldFlush := pendingDelta.Len() >= 4096
				pendingMu.Unlock()
				if shouldFlush {
					return flushDelta(runCtx)
				}
			}
			return nil
		} else {
			if err := flushDelta(runCtx); err != nil {
				return err
			}
			acquired, err := w.repo.SetAgentRun(runCtx, run.ID, w.workerID, upstreamRunID, event.Sequence, w.now().UTC())
			if err != nil {
				return err
			}
			if !acquired {
				return ErrRunLeaseLost
			}
		}
		switch event.Type {
		case "run.completed":
			if err := w.finish(runCtx, run.ID, models.RunStatusCompleted, "", "", event.Sequence); err != nil {
				return err
			}
			terminal.Store(true)
		case "run.failed":
			if err := w.finish(runCtx, run.ID, models.RunStatusFailed, "AGENT_RUN_FAILED", eventError(event), event.Sequence); err != nil {
				return err
			}
			terminal.Store(true)
		case "run.cancelled":
			if err := w.finish(runCtx, run.ID, models.RunStatusCancelled, "", "", event.Sequence); err != nil {
				return err
			}
			terminal.Store(true)
		}
		if terminal.Load() {
			return publishTerminalEvent(runCtx, w.events, run.ID, event)
		}
		_, err := w.events.Publish(runCtx, run.ID, event)
		return err
	})
	stopFlushLoop()
	<-flushLoopDone
	select {
	case periodicErr := <-periodicFlushErr:
		if err == nil || errors.Is(err, context.Canceled) {
			err = periodicErr
		}
	default:
	}
	flushCtx, flushCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	if flushErr := flushDelta(flushCtx); err == nil && flushErr != nil {
		err = flushErr
	}
	flushCancel()
	close(heartbeatDone)
	if terminal.Load() {
		return
	}
	finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer finalCancel()
	if runtimeLost.Load() || gatewayRuntimeExpired(err) {
		_ = w.repo.ExpireRuntime(finalCtx, run.OwnerID, run.ProjectID, w.now().UTC())
		w.fail(finalCtx, run, "PROJECT_RUNTIME_EXPIRED", ErrRuntimeExpired)
		return
	}
	if requested, requestErr := w.repo.IsCancelRequested(finalCtx, run.ID); requestErr == nil && requested {
		w.cancelled(finalCtx, run)
		return
	}
	if err == nil {
		err = errors.New("agent stream ended without a terminal event")
	}
	w.fail(finalCtx, run, "AGENT_STREAM_FAILED", err)
}

func (w *RunWorker) keepAlive(ctx context.Context, cancelRun context.CancelFunc, run *models.Run, runtime *models.ProjectRuntime, agentRunID *atomic.Value, runtimeLost *atomic.Bool, done <-chan struct{}) {
	heartbeat := time.NewTicker(w.heartbeat)
	cancelPoll := time.NewTicker(w.cancelPoll)
	keepAlive := time.NewTicker(5 * time.Minute)
	defer heartbeat.Stop()
	defer cancelPoll.Stop()
	defer keepAlive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-heartbeat.C:
			now := w.now().UTC()
			acquired, heartbeatErr := w.repo.Heartbeat(ctx, run.ID, w.workerID, now)
			if heartbeatErr == nil && !acquired {
				cancelRun()
				return
			}
			_ = w.repo.TouchRuntime(ctx, run.ProjectID, now)
		case <-cancelPoll.C:
			requested, err := w.repo.IsCancelRequested(ctx, run.ID)
			if err == nil && requested {
				cancelRun()
				if id, _ := agentRunID.Load().(string); id != "" {
					cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					_ = w.gateway.CancelRun(cancelCtx, runtime.GatewaySessionID, id)
					cancel()
				}
				return
			}
		case <-keepAlive.C:
			keepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, keepErr := w.gateway.EnsureRuntime(keepCtx, runtime.GatewaySessionID)
			cancel()
			if keepErr != nil {
				if gatewayRuntimeExpired(keepErr) {
					runtimeLost.Store(true)
				}
				cancelRun()
				return
			}
		}
	}
}

func (w *RunWorker) finishCancellation(ctx context.Context, run *models.Run) bool {
	requested, err := w.repo.IsCancelRequested(ctx, run.ID)
	if err != nil || !requested {
		return false
	}
	w.cancelled(ctx, run)
	return true
}

func (w *RunWorker) keepRuntimePreparationAlive(ctx context.Context, cancel context.CancelFunc, run *models.Run, leaseLost *atomic.Bool, done <-chan struct{}) {
	heartbeat := time.NewTicker(w.heartbeat)
	ticker := time.NewTicker(w.cancelPoll)
	defer heartbeat.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-heartbeat.C:
			acquired, err := w.repo.Heartbeat(ctx, run.ID, w.workerID, w.now().UTC())
			if err == nil && !acquired {
				leaseLost.Store(true)
				cancel()
				return
			}
		case <-ticker.C:
			requested, err := w.repo.IsCancelRequested(ctx, run.ID)
			if err == nil && requested {
				cancel()
				return
			}
		}
	}
}

func (w *RunWorker) confirmLease(ctx context.Context, run *models.Run) bool {
	acquired, err := w.repo.Heartbeat(ctx, run.ID, w.workerID, w.now().UTC())
	if err != nil {
		w.fail(ctx, run, "WORKER_HEARTBEAT_FAILED", err)
		return false
	}
	return acquired
}

func (w *RunWorker) finish(ctx context.Context, runID, status, code, message string, sequence int64) error {
	acquired, err := w.repo.FinishRun(ctx, runID, w.workerID, status, code, message, sequence, w.now().UTC())
	if err != nil {
		return err
	}
	if !acquired {
		return ErrRunLeaseLost
	}
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("app.run.status", status))
	if status == models.RunStatusCompleted {
		span.SetStatus(codes.Ok, "")
	} else if status == models.RunStatusFailed {
		span.SetStatus(codes.Error, code)
		if message != "" {
			span.RecordError(errors.New(message))
		}
	}
	return nil
}

func (w *RunWorker) fail(ctx context.Context, run *models.Run, code string, cause error) {
	message := ""
	if cause != nil {
		message = cause.Error()
		span := trace.SpanFromContext(ctx)
		span.RecordError(cause)
		span.SetStatus(codes.Error, code)
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("app.run.status", models.RunStatusFailed), attribute.String("app.run.error_code", code))
	now := w.now().UTC()
	sequence := run.LastSequence + 1
	acquired, err := w.repo.FinishRun(ctx, run.ID, w.workerID, models.RunStatusFailed, code, message, sequence, now)
	if err != nil || !acquired {
		return
	}
	payload, _ := json.Marshal(map[string]string{"code": code, "error": message})
	_ = publishTerminalEvent(ctx, w.events, run.ID, &models.AgentEvent{Type: "run.failed", RunID: run.ID, Sequence: sequence, Timestamp: now, Payload: payload})
}

func (w *RunWorker) cancelled(ctx context.Context, run *models.Run) {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("app.run.status", models.RunStatusCancelled))
	now := w.now().UTC()
	sequence := run.LastSequence + 1
	acquired, err := w.repo.FinishRun(ctx, run.ID, w.workerID, models.RunStatusCancelled, "", "", sequence, now)
	if err != nil || !acquired {
		return
	}
	_ = publishTerminalEvent(ctx, w.events, run.ID, &models.AgentEvent{Type: "run.cancelled", RunID: run.ID, Sequence: sequence, Timestamp: now, Payload: json.RawMessage(`{}`)})
}

func (w *RunWorker) recoverOrphans(ctx context.Context) {
	now := w.now().UTC()
	ids, err := w.repo.FailOrphanedRuns(ctx, now.Add(-w.orphanAge), now)
	if err != nil {
		zap.L().Warn("recover orphaned app runs failed", zap.Error(err))
		return
	}
	for _, run := range ids {
		payload, _ := json.Marshal(map[string]string{"code": "WORKER_HEARTBEAT_LOST", "error": "run worker heartbeat expired"})
		_ = publishTerminalEvent(ctx, w.events, run.RunID, &models.AgentEvent{Type: "run.failed", RunID: run.RunID, Sequence: run.Sequence, Timestamp: now, Payload: payload})
	}
}

func publishTerminalEvent(ctx context.Context, events RunEventStore, runID string, event *models.AgentEvent) error {
	detached := context.WithoutCancel(ctx)
	publishCtx, cancelPublish := context.WithTimeout(detached, terminalEventOperationTimeout)
	_, publishErr := events.Publish(publishCtx, runID, event)
	cancelPublish()

	expireCtx, cancelExpire := context.WithTimeout(detached, terminalEventOperationTimeout)
	expireErr := events.Expire(expireCtx, runID, terminalEventTTL)
	cancelExpire()
	return errors.Join(publishErr, expireErr)
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
