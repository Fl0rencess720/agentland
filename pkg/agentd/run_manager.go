package agentd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	runStatusRunning   = "running"
	runStatusCompleted = "completed"
	runStatusFailed    = "failed"
	runStatusCancelled = "cancelled"
)

type RunState struct {
	RunID          string `json:"run_id"`
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	LastSequence   int64  `json:"last_sequence"`
	Error          string `json:"error,omitempty"`
}

type runMetadata struct {
	RunState
	MessageSHA256 string `json:"message_sha256"`
}

type managedRun struct {
	manager       *RunManager
	state         RunState
	message       string
	messageSHA256 string
	events        []Event
	notify        chan struct{}
	mu            sync.RWMutex
}

type RunManager struct {
	agent         *Agent
	root          string
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	runs          map[string]*managedRun
	conversations map[string]string
}

func NewRunManager(agent *Agent, root string) (*RunManager, error) {
	if agent == nil {
		return nil, errors.New("agent is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &RunManager{
		agent: agent, root: filepath.Join(root, "runs"), ctx: ctx, cancel: cancel,
		runs: make(map[string]*managedRun), conversations: make(map[string]string),
	}
	if err := os.MkdirAll(manager.root, 0o700); err != nil {
		cancel()
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	if err := manager.load(); err != nil {
		cancel()
		return nil, err
	}
	return manager, nil
}

func (m *RunManager) Close() { m.cancel() }

func (m *RunManager) Start(runID, conversationID, message string, captureTrajectory bool) (*RunState, bool, error) {
	if runID == "" {
		runID = uuid.NewString()
	}
	if !validID(runID) || !validID(conversationID) {
		return nil, false, errors.New("run_id and conversation_id are invalid")
	}
	if message == "" {
		return nil, false, errors.New("message is required")
	}
	digest := sha256.Sum256([]byte(message))
	messageSHA256 := hex.EncodeToString(digest[:])

	m.mu.Lock()
	if existing := m.runs[runID]; existing != nil {
		m.mu.Unlock()
		state := existing.State()
		if state.ConversationID != conversationID || existing.messageSHA256 != messageSHA256 {
			return nil, false, errors.New("run_id was used with different input")
		}
		return &state, false, nil
	}
	if active := m.conversations[conversationID]; active != "" {
		m.mu.Unlock()
		return nil, false, ErrConversationBusy
	}
	run := &managedRun{
		manager:       m,
		state:         RunState{RunID: runID, ConversationID: conversationID, Status: runStatusRunning},
		message:       message,
		messageSHA256: messageSHA256,
		events:        make([]Event, 0, 128),
		notify:        make(chan struct{}, 1),
	}
	m.runs[runID] = run
	m.conversations[conversationID] = runID
	m.mu.Unlock()

	if err := run.persistState(); err != nil {
		m.mu.Lock()
		delete(m.runs, runID)
		delete(m.conversations, conversationID)
		m.mu.Unlock()
		return nil, false, err
	}
	go m.execute(run, captureTrajectory)
	state := run.State()
	return &state, true, nil
}

func (m *RunManager) execute(run *managedRun, captureTrajectory bool) {
	_, err := m.agent.RunWithID(m.ctx, run.state.RunID, run.state.ConversationID, run.message, captureTrajectory, run.append)
	state := run.State()
	if state.Status == runStatusRunning {
		eventType := EventRunFailed
		payload := map[string]any{"error": errorString(err)}
		if errors.Is(err, context.Canceled) {
			eventType, payload = EventRunCancelled, nil
		}
		_ = run.append(Event{
			Type: eventType, RunID: state.RunID, ConversationID: state.ConversationID,
			Sequence: state.LastSequence + 1, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Payload: payload,
		})
	}
	m.mu.Lock()
	if m.conversations[run.state.ConversationID] == run.state.RunID {
		delete(m.conversations, run.state.ConversationID)
	}
	m.mu.Unlock()
}

func (m *RunManager) Get(runID string) (*RunState, bool) {
	m.mu.Lock()
	run := m.runs[runID]
	m.mu.Unlock()
	if run == nil {
		return nil, false
	}
	state := run.State()
	return &state, true
}

func (m *RunManager) Cancel(runID string) bool {
	state, ok := m.Get(runID)
	if !ok || state.Status != runStatusRunning {
		return false
	}
	return m.agent.Cancel(runID)
}

func (m *RunManager) Subscribe(ctx context.Context, runID string, after int64, send func(Event) error, ping func() error) error {
	m.mu.Lock()
	run := m.runs[runID]
	m.mu.Unlock()
	if run == nil {
		return os.ErrNotExist
	}
	for {
		events, terminal := run.eventsAfter(after)
		for _, event := range events {
			if err := send(event); err != nil {
				return err
			}
			after = event.Sequence
		}
		if terminal {
			return nil
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-run.notify:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			if ping != nil {
				if err := ping(); err != nil {
					return err
				}
			}
		}
	}
}

func (r *managedRun) State() RunState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

func (r *managedRun) eventsAfter(after int64) ([]Event, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	start := 0
	for start < len(r.events) && r.events[start].Sequence <= after {
		start++
	}
	events := append([]Event(nil), r.events[start:]...)
	return events, terminalRunStatus(r.state.Status)
}

func (r *managedRun) append(event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.Sequence <= r.state.LastSequence {
		return nil
	}
	if event.Sequence != r.state.LastSequence+1 {
		return fmt.Errorf("run event sequence jumped from %d to %d", r.state.LastSequence, event.Sequence)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	dir := filepath.Join(r.manager.root, r.state.RunID)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	r.events = append(r.events, event)
	r.state.LastSequence = event.Sequence
	switch event.Type {
	case EventRunCompleted:
		r.state.Status = runStatusCompleted
	case EventRunCancelled:
		r.state.Status = runStatusCancelled
	case EventRunFailed:
		r.state.Status = runStatusFailed
		if payload, ok := event.Payload.(map[string]any); ok {
			r.state.Error, _ = payload["error"].(string)
		}
	}
	if err = r.persistStateLocked(); err != nil {
		return err
	}
	select {
	case r.notify <- struct{}{}:
	default:
	}
	return nil
}

func (r *managedRun) persistState() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persistStateLocked()
}

func (r *managedRun) persistStateLocked() error {
	metadata := runMetadata{RunState: r.state, MessageSHA256: r.messageSHA256}
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	dir := filepath.Join(r.manager.root, r.state.RunID)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary := filepath.Join(dir, "state.json.tmp")
	if err = os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(dir, "state.json"))
}

func (m *RunManager) load() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fmt.Errorf("read run directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(m.root, entry.Name())
		stateData, readErr := os.ReadFile(filepath.Join(dir, "state.json"))
		if readErr != nil {
			return fmt.Errorf("read run %s state: %w", entry.Name(), readErr)
		}
		var metadata runMetadata
		if err = json.Unmarshal(stateData, &metadata); err != nil {
			return fmt.Errorf("decode run %s state: %w", entry.Name(), err)
		}
		if metadata.RunID != entry.Name() || !validID(metadata.RunID) || !validID(metadata.ConversationID) || metadata.MessageSHA256 == "" {
			return fmt.Errorf("run %s metadata is invalid", entry.Name())
		}
		events, loadErr := loadEvents(filepath.Join(dir, "events.jsonl"))
		if errors.Is(loadErr, os.ErrNotExist) && metadata.LastSequence == 0 {
			events, loadErr = nil, nil
		}
		if loadErr != nil {
			return fmt.Errorf("load run %s events: %w", entry.Name(), loadErr)
		}
		if err = validateRunEvents(events, metadata.LastSequence); err != nil {
			return fmt.Errorf("validate run %s events: %w", entry.Name(), err)
		}
		run := &managedRun{
			manager: m, state: metadata.RunState, messageSHA256: metadata.MessageSHA256,
			events: events, notify: make(chan struct{}, 1),
		}
		m.runs[metadata.RunID] = run
		if metadata.Status == runStatusRunning {
			m.conversations[metadata.ConversationID] = metadata.RunID
		}
	}
	for _, run := range m.runs {
		state := run.State()
		if state.Status != runStatusRunning {
			continue
		}
		if err = run.append(Event{
			Type: EventRunFailed, RunID: state.RunID, ConversationID: state.ConversationID,
			Sequence: state.LastSequence + 1, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Payload: map[string]any{"error": "agentd restarted before the run completed"},
		}); err != nil {
			return fmt.Errorf("close interrupted run %s: %w", state.RunID, err)
		}
		delete(m.conversations, state.ConversationID)
	}
	return nil
}

func validateRunEvents(events []Event, lastSequence int64) error {
	for index, event := range events {
		expected := int64(index + 1)
		if event.Sequence != expected {
			return fmt.Errorf("sequence %d follows %d", event.Sequence, expected-1)
		}
	}
	if int64(len(events)) != lastSequence {
		return fmt.Errorf("state sequence %d differs from journal sequence %d", lastSequence, len(events))
	}
	return nil
}

func terminalRunStatus(status string) bool {
	return status == runStatusCompleted || status == runStatusFailed || status == runStatusCancelled
}

func loadEvents(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make([]Event, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxTrajectoryLineBytes+(1<<20))
	for scanner.Scan() {
		var event Event
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, scanner.Err()
}
