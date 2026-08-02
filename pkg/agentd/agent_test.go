package agentd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	modelopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type fakeModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	streamErr int
	err       error
	calls     int
}

func (f *fakeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("summary", nil), nil
}

func (f *fakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.streamErr > 0 {
		f.streamErr--
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("temporary model error")
	}
	if len(f.responses) == 0 {
		return nil, errors.New("missing fake response")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}

func TestAgentDoesNotRetryPermanentModelError(t *testing.T) {
	chatModel := &fakeModel{
		streamErr: 1,
		err:       &modelopenai.APIError{HTTPStatusCode: http.StatusBadRequest},
	}
	agent := newTestAgent(t, chatModel, nil)
	agent.retryDelay = func(int) time.Duration { return 0 }

	_, err := agent.Run(context.Background(), "permanent-error", "hello", func(Event) error { return nil })
	require.Error(t, err)
	require.Equal(t, 1, chatModel.calls)
}

func (f *fakeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}

func TestAgentRunsUntilModelStops(t *testing.T) {
	const toolCalls = 40
	responses := make([]*schema.Message, 0, toolCalls+1)
	for i := 0; i < toolCalls; i++ {
		responses = append(responses, schema.AssistantMessage("", []schema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", i),
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "echo",
				Arguments: `{"text":"ok"}`,
			},
		}}))
	}
	responses = append(responses, schema.AssistantMessage("finished", nil))
	chatModel := &fakeModel{responses: responses}
	echo, err := toolutils.InferTool("echo", "echo input", func(_ context.Context, input struct {
		Text string `json:"text"`
	}) (string, error) {
		return input.Text, nil
	})
	require.NoError(t, err)
	agent := newTestAgent(t, chatModel, []tool.BaseTool{echo})

	var events []Event
	_, err = agent.Run(context.Background(), "conversation", "start", func(event Event) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, toolCalls+1, chatModel.calls)
	require.Equal(t, EventRunCompleted, events[len(events)-1].Type)

	messages, err := agent.memory.Messages("conversation")
	require.NoError(t, err)
	require.Len(t, messages, 1+toolCalls*2+1)
	require.Equal(t, "finished", messages[len(messages)-1].Content)
}

func TestAgentRetriesModelFiveTimes(t *testing.T) {
	chatModel := &fakeModel{
		streamErr: 5,
		responses: []*schema.Message{schema.AssistantMessage("ok", nil)},
	}
	agent := newTestAgent(t, chatModel, nil)
	agent.retryDelay = func(int) time.Duration { return 0 }

	_, err := agent.Run(context.Background(), "retry", "hello", func(Event) error { return nil })
	require.NoError(t, err)
	require.Equal(t, 6, chatModel.calls)
}

func TestAgentCancelStopsRunningTool(t *testing.T) {
	started := make(chan struct{})
	blockingTool, err := toolutils.InferTool("wait", "wait until cancelled", func(ctx context.Context, _ struct{}) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	require.NoError(t, err)
	chatModel := &fakeModel{responses: []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{
		ID:   "wait-call",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "wait",
			Arguments: `{}`,
		},
	}})}}
	agent := newTestAgent(t, chatModel, []tool.BaseTool{blockingTool})
	agent.retryDelay = func(int) time.Duration { return 0 }
	events := make(chan Event, 16)
	errCh := make(chan error, 1)
	go func() {
		_, err := agent.Run(context.Background(), "cancel", "start", func(event Event) error {
			events <- event
			return nil
		})
		errCh <- err
	}()

	<-started
	first := <-events
	require.Equal(t, EventRunStarted, first.Type)
	require.True(t, agent.Cancel(first.RunID))
	require.ErrorIs(t, <-errCh, context.Canceled)

	close(events)
	foundCancelled := false
	for event := range events {
		foundCancelled = foundCancelled || event.Type == EventRunCancelled
	}
	require.True(t, foundCancelled)
}

func newTestAgent(t *testing.T, chatModel model.ToolCallingChatModel, tools []tool.BaseTool) *Agent {
	t.Helper()
	memory := NewMemoryStore(t.TempDir(), 128000)
	agent, err := NewAgent(context.Background(), chatModel, tools, memory, "test prompt")
	require.NoError(t, err)
	return agent
}
