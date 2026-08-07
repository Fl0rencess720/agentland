package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var ErrConversationBusy = errors.New("conversation already has an active run")

type Agent struct {
	baseModel      model.ToolCallingChatModel
	model          model.ToolCallingChatModel
	tools          map[string]tool.InvokableTool
	memory         *MemoryStore
	trajectory     *TrajectoryStore
	prompt         string
	modelName      string
	toolSchemaHash string

	mu              sync.Mutex
	runs            map[string]context.CancelFunc
	conversationRun map[string]string
	retryDelay      func(int) time.Duration
}

func NewAgent(ctx context.Context, chatModel model.ToolCallingChatModel, tools []tool.BaseTool, memory *MemoryStore, systemPrompt string) (*Agent, error) {
	toolInfos := make([]*schema.ToolInfo, 0, len(tools))
	toolMap := make(map[string]tool.InvokableTool, len(tools))
	for _, item := range tools {
		info, err := item.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("read tool info: %w", err)
		}
		invokable, ok := item.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("tool %s is not invokable", info.Name)
		}
		if _, exists := toolMap[info.Name]; exists {
			return nil, fmt.Errorf("duplicate tool %s", info.Name)
		}
		toolInfos = append(toolInfos, info)
		toolMap[info.Name] = invokable
	}
	boundModel, err := chatModel.WithTools(toolInfos)
	if err != nil {
		return nil, fmt.Errorf("bind tools to model: %w", err)
	}
	return &Agent{
		baseModel:       chatModel,
		model:           boundModel,
		tools:           toolMap,
		memory:          memory,
		trajectory:      NewTrajectoryStore(filepath.Dir(memory.root)),
		prompt:          systemPrompt,
		toolSchemaHash:  stableHash(toolInfos),
		runs:            make(map[string]context.CancelFunc),
		conversationRun: make(map[string]string),
		retryDelay: func(attempt int) time.Duration {
			base := time.Second << min(attempt, 4)
			return base + time.Duration(rand.Int63n(int64(base/4+1)))
		},
	}, nil
}

func (a *Agent) Run(parent context.Context, conversationID, userMessage string, emit func(Event) error) (runID string, err error) {
	return a.RunWithOptions(parent, conversationID, userMessage, false, emit)
}

func (a *Agent) RunWithOptions(parent context.Context, conversationID, userMessage string, captureTrajectory bool, emit func(Event) error) (runID string, err error) {
	if !validID(conversationID) {
		return "", fmt.Errorf("invalid conversation_id")
	}
	if strings.TrimSpace(userMessage) == "" {
		return "", fmt.Errorf("message is required")
	}
	if len(userMessage) > maxChatMessageBytes {
		return "", fmt.Errorf("message exceeds %d bytes", maxChatMessageBytes)
	}

	runID = uuid.NewString()
	ctx, cancel := context.WithCancel(parent)
	if err := a.registerRun(conversationID, runID, cancel); err != nil {
		cancel()
		return "", err
	}
	defer func() {
		cancel()
		a.unregisterRun(conversationID, runID)
	}()
	ctx, runSpan := otel.Tracer("agentland/agentd").Start(ctx, "agent.run", trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "invoke_agent"),
		attribute.String("gen_ai.agent.name", "agentd"),
		attribute.String("gen_ai.conversation.id", conversationID),
		attribute.String("gen_ai.request.model", a.modelName),
		attribute.String("agent.run.id", runID),
		attribute.String("langfuse.observation.type", "agent"),
		attribute.String("langfuse.observation.input", traceJSON(userMessage)),
		attribute.String("langfuse.session.id", conversationID),
		attribute.String("langfuse.trace.name", "agent-run"),
	))
	defer func() {
		if err != nil {
			runSpan.RecordError(err)
			runSpan.SetStatus(codes.Error, err.Error())
			runSpan.SetAttributes(attribute.String("langfuse.observation.output", traceJSON(map[string]string{"error": err.Error()})))
		}
		runSpan.End()
	}()

	events := &eventEmitter{runID: runID, conversationID: conversationID, emit: emit}
	record := func(recordType string, step int, payload any) error {
		record, err := a.trajectory.Append(runID, conversationID, recordType, step, payload)
		if err != nil {
			return err
		}
		if captureTrajectory {
			return events.send(EventTrajectoryRecord, record)
		}
		return nil
	}
	if err := record(TrajectoryRunStarted, 0, trajectoryRunStarted{
		Message: userMessage, Model: a.modelName, SystemPrompt: a.prompt, PromptHash: stableHash(a.prompt),
		ToolSchemaHash: a.toolSchemaHash, CaptureVersion: "v1",
	}); err != nil {
		return runID, err
	}
	if err := events.send(EventRunStarted, map[string]any{"message": userMessage}); err != nil {
		return runID, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if events.send(EventPing, nil) != nil {
					cancel()
				}
			}
		}
	}()

	if err := a.memory.Append(conversationID, schema.UserMessage(userMessage)); err != nil {
		_ = record(TrajectoryRunFinished, 0, trajectoryRunFinished{Status: "failed", Error: err.Error()})
		events.send(EventRunFailed, map[string]any{"error": err.Error()})
		return runID, err
	}

	for step := 1; ; step++ {
		messages, err := a.memory.Context(ctx, conversationID, a.prompt, a.baseModel)
		if err != nil {
			return runID, a.finishError(ctx, events, record, err)
		}
		if err := record(TrajectoryModelInput, step, trajectoryModelInput{Messages: messages}); err != nil {
			return runID, a.finishError(ctx, events, record, err)
		}
		response, err := a.streamModel(ctx, messages, events, step)
		if err != nil {
			return runID, a.finishError(ctx, events, record, err)
		}
		if err := record(TrajectoryModelOutput, step, trajectoryModelOutput{Message: response}); err != nil {
			return runID, a.finishError(ctx, events, record, err)
		}
		if err := a.memory.Append(conversationID, response); err != nil {
			return runID, a.finishError(ctx, events, record, err)
		}
		if len(response.ToolCalls) == 0 {
			runSpan.SetAttributes(attribute.String("langfuse.observation.output", traceJSON(response.Content)))
			if err := record(TrajectoryRunFinished, step, trajectoryRunFinished{Status: "completed", Content: response.Content}); err != nil {
				return runID, a.finishError(ctx, events, record, err)
			}
			if err := events.send(EventRunCompleted, map[string]any{"content": response.Content}); err != nil {
				return runID, err
			}
			return runID, nil
		}

		for _, call := range response.ToolCalls {
			if err := events.send(EventToolStarted, map[string]any{
				"tool_call_id": call.ID,
				"name":         call.Function.Name,
				"arguments":    call.Function.Arguments,
			}); err != nil {
				return runID, err
			}
			output, toolErr := a.invokeTool(ctx, call)
			if toolErr != nil {
				output = "tool error: " + toolErr.Error()
			}
			output = boundToolOutput(output)
			toolRecord := trajectoryToolResult{ToolCallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments, Output: output}
			if toolErr != nil {
				toolRecord.Error = toolErr.Error()
			}
			if err := record(TrajectoryToolResult, step, toolRecord); err != nil {
				return runID, a.finishError(ctx, events, record, err)
			}
			if err := emitToolOutput(events, call, output); err != nil {
				return runID, err
			}
			completed := map[string]any{"tool_call_id": call.ID, "name": call.Function.Name}
			if toolErr != nil {
				completed["error"] = toolErr.Error()
			}
			if err := events.send(EventToolCompleted, completed); err != nil {
				return runID, err
			}
			toolMessage := schema.ToolMessage(output, call.ID, schema.WithToolName(call.Function.Name))
			if err := a.memory.Append(conversationID, toolMessage); err != nil {
				return runID, a.finishError(ctx, events, record, err)
			}
		}
	}
}

func emitToolOutput(events *eventEmitter, call schema.ToolCall, output string) error {
	if output == "" {
		return events.send(EventToolOutput, map[string]any{
			"tool_call_id": call.ID,
			"name":         call.Function.Name,
			"output":       "",
		})
	}
	for len(output) != 0 {
		split := min(len(output), maxToolOutputEventBytes)
		for split < len(output) && split > 0 && !utf8.RuneStart(output[split]) {
			split--
		}
		if err := events.send(EventToolOutput, map[string]any{
			"tool_call_id": call.ID,
			"name":         call.Function.Name,
			"output":       output[:split],
		}); err != nil {
			return err
		}
		output = output[split:]
	}
	return nil
}

func (a *Agent) Cancel(runID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cancel, ok := a.runs[runID]
	if ok {
		cancel()
	}
	return ok
}

func (a *Agent) streamModel(ctx context.Context, messages []*schema.Message, events *eventEmitter, step int) (response *schema.Message, resultErr error) {
	ctx, span := otel.Tracer("agentland/agentd").Start(ctx, "model.generate", trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", a.modelName),
		attribute.String("gen_ai.input.messages", traceJSON(messages)),
		attribute.Int("agent.step", step),
		attribute.String("langfuse.observation.type", "generation"),
		attribute.String("langfuse.observation.input", traceJSON(messages)),
	))
	defer func() {
		if resultErr != nil {
			span.RecordError(resultErr)
			span.SetStatus(codes.Error, resultErr.Error())
		}
		span.End()
	}()
	for attempt := 0; attempt <= 5; attempt++ {
		stream, err := a.model.Stream(ctx, messages)
		if err != nil {
			if attempt == 5 || ctx.Err() != nil || !retryableModelError(err) {
				return nil, err
			}
			if err := sleepContext(ctx, a.retryDelay(attempt)); err != nil {
				return nil, err
			}
			continue
		}
		response, chunks, err := consumeModelStream(stream, events)
		stream.Close()
		if err == nil {
			span.SetAttributes(attribute.Int("agent.model.retry_count", attempt))
			span.SetAttributes(attribute.String("gen_ai.output.messages", traceJSON(response)))
			span.SetAttributes(attribute.String("langfuse.observation.output", traceJSON(response)))
			if response.ResponseMeta != nil {
				if response.ResponseMeta.FinishReason != "" {
					span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{response.ResponseMeta.FinishReason}))
				}
				if response.ResponseMeta.Usage != nil {
					span.SetAttributes(
						attribute.Int("gen_ai.usage.input_tokens", response.ResponseMeta.Usage.PromptTokens),
						attribute.Int("gen_ai.usage.output_tokens", response.ResponseMeta.Usage.CompletionTokens),
					)
				}
			}
			return response, nil
		}
		if chunks > 0 || attempt == 5 || ctx.Err() != nil || !retryableModelError(err) {
			return nil, err
		}
		if err := sleepContext(ctx, a.retryDelay(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("model retry loop ended unexpectedly")
}

func retryableModelError(err error) bool {
	var apiErr *modelopenai.APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatusCode == 0 {
		return true
	}
	return apiErr.HTTPStatusCode == http.StatusRequestTimeout ||
		apiErr.HTTPStatusCode == http.StatusConflict ||
		apiErr.HTTPStatusCode == http.StatusTooManyRequests ||
		apiErr.HTTPStatusCode >= http.StatusInternalServerError
}

func consumeModelStream(stream *schema.StreamReader[*schema.Message], events *eventEmitter) (*schema.Message, int, error) {
	var chunks []*schema.Message
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, len(chunks), err
		}
		chunks = append(chunks, chunk)
		if chunk.Content != "" {
			if err := events.send(EventMessageDelta, map[string]any{"content": chunk.Content}); err != nil {
				return nil, len(chunks), err
			}
		}
	}
	if len(chunks) == 0 {
		return nil, 0, fmt.Errorf("model returned an empty stream")
	}
	message, err := schema.ConcatMessages(chunks)
	return message, len(chunks), err
}

func (a *Agent) invokeTool(ctx context.Context, call schema.ToolCall) (string, error) {
	ctx, span := otel.Tracer("agentland/agentd").Start(ctx, "tool."+call.Function.Name, trace.WithAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", call.Function.Name),
		attribute.String("gen_ai.tool.call.id", call.ID),
		attribute.String("gen_ai.tool.call.arguments", call.Function.Arguments),
		attribute.String("langfuse.observation.type", "tool"),
		attribute.String("langfuse.observation.input", traceString(call.Function.Arguments)),
	))
	defer span.End()
	item, ok := a.tools[call.Function.Name]
	if !ok {
		err := fmt.Errorf("unknown tool %q", call.Function.Name)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return "", err
	}
	output, err := item.InvokableRun(ctx, call.Function.Arguments)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.SetAttributes(attribute.String("gen_ai.tool.call.result", traceString(boundToolOutput(output))))
	span.SetAttributes(attribute.String("langfuse.observation.output", traceJSON(map[string]any{"output": boundToolOutput(output), "error": errorString(err)})))
	return output, err
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *Agent) finishError(ctx context.Context, events *eventEmitter, record func(string, int, any) error, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		_ = record(TrajectoryRunFinished, 0, trajectoryRunFinished{Status: "cancelled"})
		events.send(EventRunCancelled, nil)
		return context.Canceled
	}
	_ = record(TrajectoryRunFinished, 0, trajectoryRunFinished{Status: "failed", Error: err.Error()})
	events.send(EventRunFailed, map[string]any{"error": err.Error()})
	return err
}

func (a *Agent) registerRun(conversationID, runID string, cancel context.CancelFunc) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.conversationRun[conversationID]; exists {
		return ErrConversationBusy
	}
	a.runs[runID] = cancel
	a.conversationRun[conversationID] = runID
	return nil
}

func (a *Agent) unregisterRun(conversationID, runID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.runs, runID)
	if a.conversationRun[conversationID] == runID {
		delete(a.conversationRun, conversationID)
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
