package agentd

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	EventRunStarted    = "run.started"
	EventMessageDelta  = "message.delta"
	EventToolStarted   = "tool.started"
	EventToolOutput    = "tool.output"
	EventToolCompleted = "tool.completed"
	EventRunCompleted  = "run.completed"
	EventRunFailed     = "run.failed"
	EventRunCancelled  = "run.cancelled"
	EventPing          = "ping"
)

type Event struct {
	Type           string `json:"type"`
	RunID          string `json:"run_id"`
	ConversationID string `json:"conversation_id"`
	Sequence       int64  `json:"sequence"`
	Timestamp      string `json:"timestamp"`
	Payload        any    `json:"payload,omitempty"`
}

type eventEmitter struct {
	runID          string
	conversationID string
	sequence       atomic.Int64
	closed         atomic.Bool
	mu             sync.Mutex
	emit           func(Event) error
}

func (e *eventEmitter) send(eventType string, payload any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if eventType == EventPing && e.closed.Load() {
		return nil
	}
	err := e.emit(Event{
		Type:           eventType,
		RunID:          e.runID,
		ConversationID: e.conversationID,
		Sequence:       e.sequence.Add(1),
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
		Payload:        payload,
	})
	if err == nil && (eventType == EventRunCompleted || eventType == EventRunFailed || eventType == EventRunCancelled) {
		e.closed.Store(true)
	}
	return err
}
