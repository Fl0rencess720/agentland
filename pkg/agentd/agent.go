package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

var ErrConversationBusy = errors.New("conversation already has an active run")

type Agent struct {
	baseModel model.ToolCallingChatModel
	model     model.ToolCallingChatModel
	tools     map[string]tool.InvokableTool
	memory    *MemoryStore
	prompt    string

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
		prompt:          systemPrompt,
		runs:            make(map[string]context.CancelFunc),
		conversationRun: make(map[string]string),
		retryDelay: func(attempt int) time.Duration {
			base := time.Second << min(attempt, 4)
			return base + time.Duration(rand.Int63n(int64(base/4+1)))
		},
	}, nil
}

func (a *Agent) Run(parent context.Context, conversationID, userMessage string, emit func(Event) error) (runID string, err error) {
	if !validID(conversationID) {
		return "", fmt.Errorf("invalid conversation_id")
	}
	if userMessage == "" {
		return "", fmt.Errorf("message is required")
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

	events := &eventEmitter{runID: runID, conversationID: conversationID, emit: emit}
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
		events.send(EventRunFailed, map[string]any{"error": err.Error()})
		return runID, err
	}

	for {
		messages, err := a.memory.Context(ctx, conversationID, a.prompt, a.baseModel)
		if err != nil {
			return runID, a.finishError(ctx, events, err)
		}
		response, err := a.streamModel(ctx, messages, events)
		if err != nil {
			return runID, a.finishError(ctx, events, err)
		}
		if err := a.memory.Append(conversationID, response); err != nil {
			return runID, a.finishError(ctx, events, err)
		}
		if len(response.ToolCalls) == 0 {
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
			if err := events.send(EventToolOutput, map[string]any{
				"tool_call_id": call.ID,
				"name":         call.Function.Name,
				"output":       output,
			}); err != nil {
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
				return runID, a.finishError(ctx, events, err)
			}
		}
	}
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

func (a *Agent) streamModel(ctx context.Context, messages []*schema.Message, events *eventEmitter) (*schema.Message, error) {
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
	item, ok := a.tools[call.Function.Name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", call.Function.Name)
	}
	return item.InvokableRun(ctx, call.Function.Arguments)
}

func (a *Agent) finishError(ctx context.Context, events *eventEmitter, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		events.send(EventRunCancelled, nil)
		return context.Canceled
	}
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
