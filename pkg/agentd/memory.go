package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type MemoryStore struct {
	root          string
	contextTokens int
	mu            sync.RWMutex
}

type conversationSummary struct {
	UpTo      int    `json:"up_to"`
	Content   string `json:"content"`
	UpdatedAt string `json:"updated_at"`
}

func NewMemoryStore(workspaceRoot string, contextTokens int) *MemoryStore {
	if contextTokens <= 0 {
		contextTokens = 128000
	}
	return &MemoryStore{
		root:          filepath.Join(workspaceRoot, ".agentland"),
		contextTokens: contextTokens,
	}
}

func (s *MemoryStore) Append(conversationID string, message *schema.Message) error {
	dir, err := s.conversationDir(conversationID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create conversation directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "history.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open conversation history: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append conversation history: %w", err)
	}
	return f.Sync()
}

func (s *MemoryStore) Messages(conversationID string) ([]*schema.Message, error) {
	dir, err := s.conversationDir(conversationID)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open conversation history: %w", err)
	}
	defer f.Close()

	var lines [][]byte
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read conversation history: %w", err)
	}
	var messages []*schema.Message
	for index, line := range lines {
		var message schema.Message
		if err := json.Unmarshal(line, &message); err != nil {
			if index == len(lines)-1 {
				continue
			}
			return nil, fmt.Errorf("decode conversation history line %d: %w", index+1, err)
		}
		messages = append(messages, &message)
	}
	return messages, nil
}

func (s *MemoryStore) Context(ctx context.Context, conversationID, systemPrompt string, chatModel model.BaseChatModel) ([]*schema.Message, error) {
	history, err := s.Messages(conversationID)
	if err != nil {
		return nil, err
	}
	memory, err := s.readProjectMemory()
	if err != nil {
		return nil, err
	}
	if memory != "" {
		systemPrompt += "\n\nProject memory:\n" + memory
	}

	summary, err := s.loadSummary(conversationID)
	if err != nil {
		return nil, err
	}
	start := 0
	if summary != nil && summary.UpTo <= len(history) {
		start = summary.UpTo
	}

	messages := buildContext(systemPrompt, summary, history[start:])
	if estimateTokens(messages) < int(float64(s.contextTokens)*0.7) {
		return messages, nil
	}

	keepStart := recentStart(history, start, s.contextTokens)
	if keepStart <= start {
		return messages, nil
	}
	newSummary, err := summarize(ctx, chatModel, summary, history[start:keepStart])
	if err != nil {
		return messages, nil
	}
	newSummary.UpTo = keepStart
	if err := s.saveSummary(conversationID, newSummary); err != nil {
		return nil, err
	}
	return buildContext(systemPrompt, newSummary, history[keepStart:]), nil
}

func buildContext(systemPrompt string, summary *conversationSummary, history []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(history)+2)
	result = append(result, schema.SystemMessage(systemPrompt))
	if summary != nil && strings.TrimSpace(summary.Content) != "" {
		result = append(result, schema.SystemMessage("Conversation summary:\n"+summary.Content))
	}
	return append(result, history...)
}

func recentStart(history []*schema.Message, floor, contextTokens int) int {
	budgetChars := contextTokens
	chars := 0
	start := len(history)
	for start > floor {
		next := chars + messageSize(history[start-1])
		if next > budgetChars && start < len(history) {
			break
		}
		chars = next
		start--
	}
	for start > floor && start < len(history) && history[start].Role != schema.User {
		start--
	}
	return start
}

func summarize(ctx context.Context, chatModel model.BaseChatModel, previous *conversationSummary, messages []*schema.Message) (*conversationSummary, error) {
	var transcript strings.Builder
	if previous != nil && previous.Content != "" {
		transcript.WriteString("Previous summary:\n")
		transcript.WriteString(previous.Content)
		transcript.WriteString("\n\nNew messages:\n")
	}
	for _, message := range messages {
		fmt.Fprintf(&transcript, "%s: %s\n", message.Role, message.Content)
		for _, call := range message.ToolCalls {
			fmt.Fprintf(&transcript, "tool_call %s(%s)\n", call.Function.Name, call.Function.Arguments)
		}
	}
	response, err := chatModel.Generate(ctx, []*schema.Message{
		schema.SystemMessage("Summarize the coding session. Preserve requirements, decisions, changed files, command results, unresolved errors, and next steps. Be concise and factual."),
		schema.UserMessage(transcript.String()),
	})
	if err != nil {
		return nil, fmt.Errorf("summarize conversation: %w", err)
	}
	return &conversationSummary{Content: response.Content, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func estimateTokens(messages []*schema.Message) int {
	chars := 0
	for _, message := range messages {
		chars += messageSize(message)
	}
	return chars/4 + 1
}

func messageSize(message *schema.Message) int {
	if message == nil {
		return 0
	}
	size := len(message.Content) + len(message.ReasoningContent)
	for _, call := range message.ToolCalls {
		size += len(call.Function.Name) + len(call.Function.Arguments)
	}
	return size
}

func (s *MemoryStore) conversationDir(conversationID string) (string, error) {
	if !validID(conversationID) {
		return "", fmt.Errorf("invalid conversation_id")
	}
	return filepath.Join(s.root, "conversations", conversationID), nil
}

func (s *MemoryStore) readProjectMemory() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(filepath.Join(s.root, "MEMORY.md"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read project memory: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *MemoryStore) loadSummary(conversationID string) (*conversationSummary, error) {
	dir, err := s.conversationDir(conversationID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read conversation summary: %w", err)
	}
	var summary conversationSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, fmt.Errorf("decode conversation summary: %w", err)
	}
	return &summary, nil
}

func (s *MemoryStore) saveSummary(conversationID string, summary *conversationSummary) error {
	dir, err := s.conversationDir(conversationID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal conversation summary: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create conversation directory: %w", err)
	}
	tmp := filepath.Join(dir, ".summary.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write conversation summary: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "summary.json")); err != nil {
		return fmt.Errorf("replace conversation summary: %w", err)
	}
	return nil
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
