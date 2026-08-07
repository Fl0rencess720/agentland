package agentd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	maxHistoryLineBytes     = 2 << 20
	maxReturnedHistoryBytes = 16 << 20
	maxReturnedMessages     = 10_000
	maxContextHistoryChars  = 8 << 20
	maxContextMessages      = 4096
	maxProjectMemoryBytes   = 64 << 10
	maxSummaryContentBytes  = 64 << 10
	maxSummaryFileBytes     = 128 << 10
	minStreamBufferChars    = 64 << 10
)

type MemoryStore struct {
	root          string
	contextTokens int
	mu            sync.RWMutex
}

type conversationSummary struct {
	UpTo      int    `json:"up_to"`
	Offset    int64  `json:"offset,omitempty"`
	Content   string `json:"content"`
	UpdatedAt string `json:"updated_at"`
}

type contextMessage struct {
	message    *schema.Message
	nextOffset int64
}

type historyLine struct {
	data       []byte
	number     int
	nextOffset int64
	terminated bool
	last       bool
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
	if len(data) > maxHistoryLineBytes {
		return fmt.Errorf("message exceeds %d bytes", maxHistoryLineBytes)
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

	messages := make([]*schema.Message, 0)
	totalBytes := 0
	err = scanHistory(f, 0, 0, func(line historyLine) error {
		var message schema.Message
		if err := json.Unmarshal(line.data, &message); err != nil {
			if line.last && !line.terminated {
				return nil
			}
			return fmt.Errorf("decode conversation history line %d: %w", line.number, err)
		}
		if len(messages) >= maxReturnedMessages || totalBytes+len(line.data) > maxReturnedHistoryBytes {
			return fmt.Errorf("conversation history exceeds %d messages or %d bytes", maxReturnedMessages, maxReturnedHistoryBytes)
		}
		messages = append(messages, &message)
		totalBytes += len(line.data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read conversation history: %w", err)
	}
	return messages, nil
}

func (s *MemoryStore) Context(ctx context.Context, conversationID, systemPrompt string, chatModel model.BaseChatModel) ([]*schema.Message, error) {
	summary, err := s.loadSummary(conversationID)
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

	historyBudget := contextHistoryBudget(s.contextTokens, systemPrompt, summary)
	history, updatedSummary, changed, err := s.contextHistory(ctx, conversationID, chatModel, summary, historyBudget)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := s.saveSummary(conversationID, updatedSummary); err != nil {
			return nil, err
		}
	}
	return buildContext(systemPrompt, updatedSummary, history), nil
}

func (s *MemoryStore) contextHistory(
	ctx context.Context,
	conversationID string,
	chatModel model.BaseChatModel,
	summary *conversationSummary,
	historyBudget int,
) ([]*schema.Message, *conversationSummary, bool, error) {
	dir, err := s.conversationDir(conversationID)
	if err != nil {
		return nil, summary, false, err
	}
	f, err := os.Open(filepath.Join(dir, "history.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, summary, false, nil
	}
	if err != nil {
		return nil, summary, false, fmt.Errorf("open conversation history: %w", err)
	}
	defer f.Close()

	checkpoint := 0
	offset := int64(0)
	if summary != nil {
		checkpoint = summary.UpTo
		offset = summary.Offset
	}
	if checkpoint < 0 || offset < 0 {
		return nil, summary, false, fmt.Errorf("conversation summary checkpoint is invalid")
	}
	if err := validateHistoryOffset(f, offset); err != nil {
		return nil, summary, false, err
	}

	legacySkip := 0
	startNumber := checkpoint
	changed := false
	if checkpoint > 0 && offset == 0 {
		legacySkip = checkpoint
		startNumber = 0
		changed = true
	}

	currentSummary := summary
	entries := make([]contextMessage, 0)
	historyChars := 0
	streamLimit := min(maxContextHistoryChars, max(minStreamBufferChars, historyBudget*2))
	err = scanHistory(f, offset, startNumber, func(line historyLine) error {
		if legacySkip > 0 {
			legacySkip--
			if legacySkip == 0 && currentSummary != nil {
				currentSummary.Offset = line.nextOffset
			}
			return nil
		}

		var message schema.Message
		if err := json.Unmarshal(line.data, &message); err != nil {
			if line.last && !line.terminated {
				return nil
			}
			return fmt.Errorf("decode conversation history line %d: %w", line.number, err)
		}
		entries = append(entries, contextMessage{message: &message, nextOffset: line.nextOffset})
		historyChars += messageSize(&message)
		if historyChars <= streamLimit && len(entries) <= maxContextMessages*2 {
			return nil
		}

		var compacted bool
		currentSummary, entries, historyChars, compacted, err = compactContext(ctx, chatModel, currentSummary, entries, historyChars, historyBudget)
		changed = changed || compacted
		return err
	})
	if err != nil {
		return nil, summary, false, fmt.Errorf("read conversation history: %w", err)
	}
	if legacySkip > 0 {
		return nil, summary, false, fmt.Errorf("conversation summary checkpoint exceeds history")
	}

	var compacted bool
	currentSummary, entries, historyChars, compacted, err = compactContext(ctx, chatModel, currentSummary, entries, historyChars, historyBudget)
	if err != nil {
		return nil, summary, false, err
	}
	changed = changed || compacted
	history := make([]*schema.Message, 0, len(entries))
	for _, entry := range entries {
		history = append(history, entry.message)
	}
	return history, currentSummary, changed, nil
}

func compactContext(
	ctx context.Context,
	chatModel model.BaseChatModel,
	summary *conversationSummary,
	entries []contextMessage,
	historyChars int,
	historyBudget int,
) (*conversationSummary, []contextMessage, int, bool, error) {
	if historyChars <= historyBudget && len(entries) <= maxContextMessages {
		return summary, entries, historyChars, false, nil
	}
	messages := make([]*schema.Message, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, entry.message)
	}
	keepStart := recentStart(messages, 0, historyBudget)
	if len(messages)-keepStart > maxContextMessages {
		keepStart = len(messages) - maxContextMessages
		keepStart = safeContextStart(messages, keepStart)
	}
	if keepStart <= 0 {
		return summary, entries, historyChars, false, nil
	}

	nextSummary, err := summarize(ctx, chatModel, summary, messages[:keepStart])
	if err != nil {
		return summary, entries, historyChars, false, err
	}
	nextSummary.UpTo = keepStart
	if summary != nil {
		nextSummary.UpTo += summary.UpTo
	}
	nextSummary.Offset = entries[keepStart-1].nextOffset
	entries = entries[keepStart:]
	historyChars = 0
	for _, entry := range entries {
		historyChars += messageSize(entry.message)
	}
	return nextSummary, entries, historyChars, true, nil
}

func buildContext(systemPrompt string, summary *conversationSummary, history []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(history)+2)
	result = append(result, schema.SystemMessage(systemPrompt))
	if summary != nil && strings.TrimSpace(summary.Content) != "" {
		result = append(result, schema.SystemMessage("Conversation summary:\n"+summary.Content))
	}
	return append(result, history...)
}

func recentStart(history []*schema.Message, floor, budgetChars int) int {
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
	return safeContextStart(history, start)
}

func safeContextStart(history []*schema.Message, start int) int {
	if start <= 0 || start >= len(history) {
		return start
	}
	original := start
	for start > 0 && history[start].Role == schema.Tool {
		start--
	}
	if len(history)-start <= maxContextMessages {
		return start
	}
	start = original
	for start < len(history) && history[start].Role == schema.Tool {
		start++
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
	content := strings.ToValidUTF8(response.Content, "\uFFFD")
	content = utf8Prefix(content, maxSummaryContentBytes)
	return &conversationSummary{Content: content, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func contextHistoryBudget(contextTokens int, systemPrompt string, summary *conversationSummary) int {
	tokens := int64(contextTokens)
	if tokens < 1 {
		tokens = 1
	}
	budget := int64(maxContextHistoryChars)
	if tokens < int64(maxContextHistoryChars)*10/(4*7) {
		budget = tokens * 4 * 7 / 10
	}
	budget -= int64(len(systemPrompt))
	if summary != nil {
		budget -= int64(len(summary.Content))
	}
	if budget < 1 {
		return 1
	}
	return int(budget)
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
	size := 16 + len(message.Content) + len(message.ReasoningContent)
	for _, call := range message.ToolCalls {
		size += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	return size
}

func scanHistory(file *os.File, offset int64, startNumber int, visit func(historyLine) error) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxHistoryLineBytes+1)
	scanner.Split(splitJSONLine)
	currentOffset := offset
	lineNumber := startNumber
	var pending *historyLine
	for scanner.Scan() {
		data := append([]byte(nil), scanner.Bytes()...)
		nextOffset := currentOffset + int64(len(data))
		terminated := nextOffset < info.Size()
		if terminated {
			nextOffset++
		}
		lineNumber++
		line := &historyLine{data: data, number: lineNumber, nextOffset: nextOffset, terminated: terminated}
		if pending != nil {
			if err := visit(*pending); err != nil {
				return err
			}
		}
		pending = line
		currentOffset = nextOffset
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("history line exceeds %d bytes or cannot be read: %w", maxHistoryLineBytes, err)
	}
	if pending != nil {
		pending.last = true
		if err := visit(*pending); err != nil {
			return err
		}
	}
	return nil
}

func splitJSONLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index], nil
	}
	if atEOF && len(data) != 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func validateHistoryOffset(file *os.File, offset int64) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if offset > info.Size() {
		return fmt.Errorf("conversation summary offset exceeds history")
	}
	if offset == 0 {
		return nil
	}
	var previous [1]byte
	if _, err := file.ReadAt(previous[:], offset-1); err != nil {
		return fmt.Errorf("validate conversation summary offset: %w", err)
	}
	if previous[0] != '\n' {
		return fmt.Errorf("conversation summary offset is not a message boundary")
	}
	return nil
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
	data, err := readLimitedFile(filepath.Join(s.root, "MEMORY.md"), maxProjectMemoryBytes)
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
	data, err := readLimitedFile(filepath.Join(dir, "summary.json"), maxSummaryFileBytes)
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
	if summary.UpTo < 0 || summary.Offset < 0 || len(summary.Content) > maxSummaryContentBytes {
		return nil, fmt.Errorf("conversation summary exceeds limits")
	}
	return &summary, nil
}

func (s *MemoryStore) saveSummary(conversationID string, summary *conversationSummary) error {
	if summary == nil {
		return nil
	}
	dir, err := s.conversationDir(conversationID)
	if err != nil {
		return err
	}
	summary.Content = utf8Prefix(strings.ToValidUTF8(summary.Content, "\uFFFD"), maxSummaryContentBytes)
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal conversation summary: %w", err)
	}
	if len(data) > maxSummaryFileBytes {
		return fmt.Errorf("conversation summary exceeds %d bytes", maxSummaryFileBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create conversation directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".summary-*.json")
	if err != nil {
		return fmt.Errorf("create conversation summary: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect conversation summary: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write conversation summary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync conversation summary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close conversation summary: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, "summary.json")); err != nil {
		return fmt.Errorf("replace conversation summary: %w", err)
	}
	return nil
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 128 {
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
