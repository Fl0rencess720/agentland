package biz

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

const assistantDeltaFlushInterval = 250 * time.Millisecond

type RunWorker struct {
	repo       RunWorkerRepo
	gateway    AgentlandGateway
	queue      TaskQueue
	workerID   string
	now        func() time.Time
	heartbeat  time.Duration
	cancelPoll time.Duration
	orphanAge  time.Duration
	runtimeMax time.Duration
	parallel   int
}

func NewRunWorker(repo RunWorkerRepo, gateway AgentlandGateway, queues ...TaskQueue) *RunWorker {
	worker := &RunWorker{
		repo: repo, gateway: gateway, workerID: token.NewID("worker"), now: time.Now,
		heartbeat:  configDuration("worker.heartbeat_interval", 5*time.Second),
		cancelPoll: configDuration("worker.cancel_poll_interval", 250*time.Millisecond),
		orphanAge:  configDuration("worker.orphan_timeout", 30*time.Second),
		runtimeMax: configDuration("runtime.max_session_duration", time.Hour),
		parallel:   configInt("worker.parallelism", 4),
	}
	if len(queues) != 0 {
		worker.queue = queues[0]
	}
	return worker
}

func (w *RunWorker) Run(ctx context.Context) {
	if w.queue == nil {
		zap.L().Error("run task queue is not configured")
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
				zap.L().Warn("receive queued app run failed", zap.Error(err))
				waitWorkerRetry(ctx, time.Second)
			}
			continue
		}
		var run *models.Run
		for ctx.Err() == nil {
			run, err = w.repo.ClaimRun(ctx, delivery.ID(), w.workerID, w.now().UTC())
			if err != nil {
				zap.L().Warn("claim queued app run failed", zap.String("run_id", delivery.ID()), zap.Error(err))
				waitWorkerRetry(ctx, 250*time.Millisecond)
				continue
			}
			break
		}
		for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
			if err = delivery.Ack(ctx); err == nil {
				break
			}
			zap.L().Warn("commit app run delivery failed", zap.String("run_id", delivery.ID()), zap.Error(err))
			waitWorkerRetry(ctx, 250*time.Millisecond)
		}
		if ctx.Err() != nil {
			<-semaphore
			return
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
	if !w.captureWorkspaceSnapshot(parent, run, runtime.GatewaySessionID) {
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
	var trajectorySequence int64
	var trajectoryHash string
	var pendingMu sync.Mutex
	flushDelta := func(ctx context.Context) error {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		if pendingDelta.Len() == 0 && len(pendingEvents) == 0 {
			return nil
		}
		if pendingDelta.Len() != 0 {
			delta := pendingDelta.String()
			acquired, err := w.repo.AppendAssistantDelta(ctx, run.ID, w.workerID, delta, pendingSequence, w.now().UTC(), pendingEvents)
			if err != nil {
				return err
			}
			if !acquired {
				return ErrRunLeaseLost
			}
			pendingDelta.Reset()
		}
		pendingEvents = pendingEvents[:0]
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
			if !isTerminalEvent(event.Type) {
				persistedEvent := event
				if event.Type == "trajectory.record" {
					persistedEvent = nil
				}
				acquired, err := w.repo.SetAgentRun(runCtx, run.ID, w.workerID, upstreamRunID, event.Sequence, w.now().UTC(), persistedEvent)
				if err != nil {
					return err
				}
				if !acquired {
					return ErrRunLeaseLost
				}
			}
		}
		if event.Type == "trajectory.record" {
			artifacts, ok := w.repo.(RunArtifactRepo)
			if !ok {
				return errors.New("run repository does not support trajectory records")
			}
			var record models.RunTrajectoryRecord
			if err := json.Unmarshal(event.Payload, &record); err != nil {
				return err
			}
			if err := verifyTrajectoryRecord(&record, upstreamRunID, runtime.AgentConversationID, trajectorySequence, trajectoryHash); err != nil {
				return err
			}
			acquired, err := artifacts.AppendTrajectoryRecord(runCtx, run.ID, w.workerID, record.Hash, record.Sequence, event.Payload, w.now().UTC())
			if err != nil {
				return err
			}
			if !acquired {
				return ErrRunLeaseLost
			}
			trajectorySequence, trajectoryHash = record.Sequence, record.Hash
			return nil
		}
		switch event.Type {
		case "run.completed":
			if err := w.finish(runCtx, run.ID, models.RunStatusCompleted, "", "", event.Sequence, event); err != nil {
				return err
			}
			terminal.Store(true)
		case "run.failed":
			if err := w.finish(runCtx, run.ID, models.RunStatusFailed, "AGENT_RUN_FAILED", eventError(event), event.Sequence, event); err != nil {
				return err
			}
			terminal.Store(true)
		case "run.cancelled":
			if err := w.finish(runCtx, run.ID, models.RunStatusCancelled, "", "", event.Sequence, event); err != nil {
				return err
			}
			terminal.Store(true)
		}
		return nil
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

func (w *RunWorker) captureWorkspaceSnapshot(ctx context.Context, run *models.Run, sessionID string) bool {
	artifacts, repoOK := w.repo.(RunArtifactRepo)
	replayGateway, gatewayOK := w.gateway.(ReplayGateway)
	if !repoOK || !gatewayOK {
		return true
	}
	snapshot, captureErr := replayGateway.GetWorkspaceSnapshot(ctx, sessionID)
	captureError := ""
	sha := ""
	if captureErr != nil {
		captureError = captureErr.Error()
		snapshot = []byte{}
	} else {
		digest := sha256.Sum256(snapshot)
		sha = fmt.Sprintf("%x", digest[:])
	}
	acquired, err := artifacts.SaveWorkspaceSnapshot(ctx, run.ID, w.workerID, snapshot, sha, captureError, w.now().UTC())
	if err != nil {
		w.fail(ctx, run, "TRAJECTORY_SNAPSHOT_FAILED", err)
		return false
	}
	if !acquired {
		return false
	}
	return true
}

func verifyTrajectoryRecord(record *models.RunTrajectoryRecord, upstreamRunID, conversationID string, previousSequence int64, previousHash string) error {
	if record.Version != 1 || record.RunID != upstreamRunID || record.ConversationID != conversationID {
		return errors.New("trajectory record identity is invalid")
	}
	if record.Sequence != previousSequence+1 || record.PreviousHash != previousHash || record.Hash == "" {
		return errors.New("trajectory record chain is invalid")
	}
	expectedHash := record.Hash
	copy := *record
	copy.Hash = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if fmt.Sprintf("%x", digest[:]) != expectedHash {
		return errors.New("trajectory record hash is invalid")
	}
	return nil
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
			if heartbeatErr != nil {
				zap.L().Warn("renew run worker lease failed", zap.String("run_id", run.ID), zap.Error(heartbeatErr))
			} else if !acquired {
				cancelRun()
				return
			}
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
			if keepErr == nil {
				_ = w.repo.TouchRuntime(keepCtx, run.ProjectID, w.now().UTC())
			}
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
			if err != nil {
				zap.L().Warn("renew run worker lease during runtime preparation failed", zap.String("run_id", run.ID), zap.Error(err))
			} else if !acquired {
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
		zap.L().Warn("confirm run worker lease failed", zap.String("run_id", run.ID), zap.Error(err))
		return true
	}
	return acquired
}

func (w *RunWorker) finish(ctx context.Context, runID, status, code, message string, sequence int64, event *models.AgentEvent) error {
	acquired, err := w.repo.FinishRun(ctx, runID, w.workerID, status, code, message, sequence, w.now().UTC(), event)
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
	payload, _ := json.Marshal(map[string]string{"code": code, "error": message})
	event := &models.AgentEvent{Type: "run.failed", RunID: run.ID, Sequence: sequence, Timestamp: now, Payload: payload}
	acquired, err := w.repo.FinishRun(ctx, run.ID, w.workerID, models.RunStatusFailed, code, message, sequence, now, event)
	if err != nil || !acquired {
		return
	}
}

func (w *RunWorker) cancelled(ctx context.Context, run *models.Run) {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("app.run.status", models.RunStatusCancelled))
	now := w.now().UTC()
	sequence := run.LastSequence + 1
	event := &models.AgentEvent{Type: "run.cancelled", RunID: run.ID, Sequence: sequence, Timestamp: now, Payload: json.RawMessage(`{}`)}
	acquired, err := w.repo.FinishRun(ctx, run.ID, w.workerID, models.RunStatusCancelled, "", "", sequence, now, event)
	if err != nil || !acquired {
		return
	}
}

func (w *RunWorker) recoverOrphans(ctx context.Context) {
	now := w.now().UTC()
	ids, err := w.repo.FailOrphanedRuns(ctx, now.Add(-w.orphanAge), now)
	if err != nil {
		zap.L().Warn("recover orphaned app runs failed", zap.Error(err))
		return
	}
	if len(ids) != 0 {
		zap.L().Warn("failed orphaned app runs", zap.Int("count", len(ids)))
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
