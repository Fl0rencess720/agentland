package agentd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
	minStreamBufferTokens   = 16 << 10

	compressionThresholdPercent   = 50
	compressionPreservePercent    = 30
	compressionRetryGrowthPercent = 150
	compressionToolTokenBudget    = 50_000
	compressionToolTailLines      = 30
	compressionToolTailBytes      = 8 << 10
	compressionMaxOutputTokens    = 8 << 10
)

const compressionSystemPrompt = `Create a compact, factual state snapshot for a coding agent that will continue this session.
Output only one <state_snapshot> block. Preserve exact user requirements and constraints, decisions and their reasons, current implementation state, changed and relevant files, commands and tool evidence, unresolved errors, and the next concrete steps.
Merge every still-relevant fact from the previous snapshot. Prefer exact paths, identifiers, error messages, and observed results. Do not invent completed work or hide uncertainty.`

type MemoryStore struct {
	root                string
	contextTokens       int
	compressionModel    string
	compressionFailures map[string]int
	mu                  sync.RWMutex
}

type conversationSummary struct {
	Version   int    `json:"version,omitempty"`
	UpTo      int    `json:"up_to"`
	Offset    int64  `json:"offset,omitempty"`
	Content   string `json:"content"`
	UpdatedAt string `json:"updated_at"`
}

type contextMessage struct {
	message    *schema.Message
	nextOffset int64
	tokens     int
	bytes      int
}

type historyLine struct {
	data       []byte
	number     int
	nextOffset int64
	terminated bool
	last       bool
}

func NewMemoryStore(workspaceRoot string, contextTokens int, compressionModels ...string) *MemoryStore {
	if contextTokens <= 0 {
		contextTokens = 128000
	}
	compressionModel := ""
	if len(compressionModels) != 0 {
		compressionModel = strings.TrimSpace(compressionModels[0])
	}
	return &MemoryStore{
		root:                filepath.Join(workspaceRoot, ".agentland"),
		contextTokens:       contextTokens,
		compressionModel:    compressionModel,
		compressionFailures: make(map[string]int),
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

	historyBudget := contextHistoryTokenBudget(s.contextTokens, systemPrompt, summary)
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
	historyTokens := 0
	historyBytes := 0
	streamTokenLimit := max(minStreamBufferTokens, historyBudget*2)
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
		entry := contextMessage{
			message: &message, nextOffset: line.nextOffset,
			tokens: estimateMessageTokens(&message), bytes: messageSize(&message),
		}
		entries = append(entries, entry)
		historyTokens += entry.tokens
		historyBytes += entry.bytes
		if historyTokens <= streamTokenLimit && historyBytes <= maxContextHistoryChars && len(entries) <= maxContextMessages*2 {
			return nil
		}

		var compacted bool
		currentSummary, entries, historyTokens, historyBytes, compacted, err = s.compactContext(
			ctx, conversationID, chatModel, currentSummary, entries, historyTokens, historyBytes, historyBudget,
		)
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
	currentSummary, entries, historyTokens, historyBytes, compacted, err = s.compactContext(
		ctx, conversationID, chatModel, currentSummary, entries, historyTokens, historyBytes, historyBudget,
	)
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

func (s *MemoryStore) compactContext(
	ctx context.Context,
	conversationID string,
	chatModel model.BaseChatModel,
	summary *conversationSummary,
	entries []contextMessage,
	historyTokens int,
	historyBytes int,
	historyBudget int,
) (*conversationSummary, []contextMessage, int, int, bool, error) {
	if historyTokens <= historyBudget && historyBytes <= maxContextHistoryChars && len(entries) <= maxContextMessages {
		return summary, entries, historyTokens, historyBytes, false, nil
	}

	ctx, span := otel.Tracer("agentland/agentd").Start(ctx, "memory.compact")
	defer span.End()
	span.SetAttributes(
		attribute.Int("agent.memory.tokens_before", historyTokens),
		attribute.Int("agent.memory.messages_before", len(entries)),
		attribute.Int("agent.memory.threshold_tokens", historyBudget),
	)

	original := make([]*schema.Message, 0, len(entries))
	for _, entry := range entries {
		original = append(original, entry.message)
	}
	degraded, err := s.degradeToolOutputs(conversationID, original)
	if err != nil {
		return summary, entries, historyTokens, historyBytes, false, err
	}
	degradedEntries, degradedTokens, degradedBytes := replaceContextMessages(entries, degraded)

	if s.skipFailedCompression(conversationID, historyTokens) {
		span.SetAttributes(attribute.String("agent.memory.compression_status", "tool_outputs_only"))
		return summary, degradedEntries, degradedTokens, degradedBytes, false, nil
	}

	keepStart := compressionSplitPoint(degraded, compressionPreservePercent)
	if len(degraded)-keepStart > maxContextMessages {
		keepStart = safeCompressionStart(degraded, len(degraded)-maxContextMessages)
	}
	if keepStart <= 0 {
		span.SetAttributes(attribute.String("agent.memory.compression_status", "no_safe_boundary"))
		return summary, degradedEntries, degradedTokens, degradedBytes, false, nil
	}

	historyForSummary := original[:keepStart]
	if estimateTokens(historyForSummary) >= s.contextTokens*9/10 {
		historyForSummary = degraded[:keepStart]
	}
	nextSummary, err := summarize(ctx, chatModel, s.compressionModel, summary, historyForSummary)
	if err != nil {
		s.recordCompressionFailure(conversationID, historyTokens)
		span.RecordError(err)
		span.SetAttributes(attribute.String("agent.memory.compression_status", "summary_failed"))
		return summary, degradedEntries, degradedTokens, degradedBytes, false, nil
	}
	nextSummary.Version = 2
	nextSummary.UpTo = keepStart
	if summary != nil {
		nextSummary.UpTo += summary.UpTo
	}
	nextSummary.Offset = entries[keepStart-1].nextOffset
	keptEntries := degradedEntries[keepStart:]
	keptTokens, keptBytes := contextMessageTotals(keptEntries)
	oldTotal := historyTokens + summaryTokenCount(summary)
	newTotal := keptTokens + summaryTokenCount(nextSummary)
	if newTotal >= oldTotal {
		s.recordCompressionFailure(conversationID, historyTokens)
		span.SetAttributes(attribute.String("agent.memory.compression_status", "summary_inflated"))
		return summary, degradedEntries, degradedTokens, degradedBytes, false, nil
	}
	s.clearCompressionFailure(conversationID)
	span.SetAttributes(
		attribute.String("agent.memory.compression_status", "completed"),
		attribute.Int("agent.memory.tokens_after", newTotal),
		attribute.Int("agent.memory.messages_after", len(keptEntries)),
	)
	return nextSummary, keptEntries, keptTokens, keptBytes, true, nil
}

func buildContext(systemPrompt string, summary *conversationSummary, history []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(history)+3)
	result = append(result, schema.SystemMessage(systemPrompt))
	if summary != nil && strings.TrimSpace(summary.Content) != "" {
		result = append(result,
			schema.UserMessage("Conversation state snapshot:\n"+summary.Content),
			schema.AssistantMessage("Understood. I will continue from this state.", nil),
		)
	}
	return append(result, history...)
}

func compressionSplitPoint(history []*schema.Message, preservePercent int) int {
	if len(history) == 0 {
		return 0
	}
	total := estimateTokens(history)
	target := total * (100 - preservePercent) / 100
	consumed, lastSafe := 0, 0
	for index, message := range history {
		if index > 0 && isCompressionBoundary(message) {
			if consumed >= target {
				return index
			}
			lastSafe = index
		}
		consumed += estimateMessageTokens(message)
	}
	last := history[len(history)-1]
	if last.Role == schema.Assistant && len(last.ToolCalls) == 0 {
		return len(history)
	}
	return lastSafe
}

func safeCompressionStart(history []*schema.Message, start int) int {
	if start <= 0 || start >= len(history) {
		return start
	}
	for index := start; index < len(history); index++ {
		if isCompressionBoundary(history[index]) {
			return index
		}
	}
	return compressionSplitPoint(history, compressionPreservePercent)
}

func isCompressionBoundary(message *schema.Message) bool {
	if message == nil {
		return false
	}
	return message.Role == schema.User || message.Role == schema.Assistant && len(message.ToolCalls) != 0
}

func summarize(
	ctx context.Context,
	chatModel model.BaseChatModel,
	compressionModel string,
	previous *conversationSummary,
	messages []*schema.Message,
) (*conversationSummary, error) {
	prompt := "Generate a new <state_snapshot> from the session history."
	if previous != nil && strings.TrimSpace(previous.Content) != "" {
		prompt = "Update this previous snapshot with the session history. Preserve all still-relevant facts:\n\n" + previous.Content
	}
	input := make([]*schema.Message, 0, len(messages)+2)
	input = append(input, schema.SystemMessage(compressionSystemPrompt))
	input = append(input, cloneMessages(messages)...)
	input = append(input, schema.UserMessage(prompt))

	options := []model.Option{model.WithTemperature(0), model.WithMaxTokens(compressionMaxOutputTokens)}
	if compressionModel != "" {
		options = append(options, model.WithModel(compressionModel))
	}
	response, err := chatModel.Generate(ctx, input, options...)
	if err != nil {
		return nil, fmt.Errorf("generate conversation state snapshot: %w", err)
	}
	draft := normalizeStateSnapshot(response.Content)
	if draft == "" {
		return nil, errors.New("generate conversation state snapshot: empty response")
	}

	verification := append(cloneMessages(input), schema.AssistantMessage(draft, nil), schema.UserMessage(
		"Check the state snapshot against the history for missing user constraints, file paths, technical details, tool evidence, unresolved errors, or next steps. Return only the final corrected <state_snapshot> block.",
	))
	verified, verifyErr := chatModel.Generate(ctx, verification, options...)
	if verifyErr == nil && strings.TrimSpace(verified.Content) != "" {
		draft = normalizeStateSnapshot(verified.Content)
	}
	return &conversationSummary{
		Content:   utf8Prefix(draft, maxSummaryContentBytes),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func normalizeStateSnapshot(content string) string {
	content = strings.TrimSpace(strings.ToValidUTF8(content, "\uFFFD"))
	if content == "" {
		return ""
	}
	if start := strings.Index(content, "<state_snapshot>"); start >= 0 {
		if end := strings.Index(content[start:], "</state_snapshot>"); end >= 0 {
			end += start + len("</state_snapshot>")
			return content[start:end]
		}
	}
	return "<state_snapshot>\n" + content + "\n</state_snapshot>"
}

func contextHistoryTokenBudget(contextTokens int, systemPrompt string, summary *conversationSummary) int {
	threshold := max(1, contextTokens*compressionThresholdPercent/100)
	budget := threshold - estimateTextTokens(systemPrompt) - summaryTokenCount(summary)
	return max(1, budget)
}

func estimateTokens(messages []*schema.Message) int {
	tokens := 0
	for _, message := range messages {
		tokens += estimateMessageTokens(message)
	}
	return max(1, tokens)
}

func estimateMessageTokens(message *schema.Message) int {
	if message == nil {
		return 0
	}
	tokens := 4 + estimateTextTokens(message.Content) + estimateTextTokens(message.ReasoningContent)
	tokens += estimateTextTokens(message.Name) + estimateTextTokens(message.ToolCallID) + estimateTextTokens(message.ToolName)
	for _, call := range message.ToolCalls {
		tokens += 4 + estimateTextTokens(call.ID) + estimateTextTokens(call.Function.Name) + estimateTextTokens(call.Function.Arguments)
	}
	return max(1, tokens)
}

func estimateTextTokens(value string) int {
	ascii, other := 0, 0
	for _, r := range value {
		if r <= unicode.MaxASCII {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}

func summaryTokenCount(summary *conversationSummary) int {
	if summary == nil {
		return 0
	}
	return estimateTextTokens(summary.Content)
}

func (s *MemoryStore) degradeToolOutputs(conversationID string, messages []*schema.Message) ([]*schema.Message, error) {
	result := cloneMessages(messages)
	used := 0
	for index := len(result) - 1; index >= 0; index-- {
		message := result[index]
		if message.Role != schema.Tool || message.Content == "" {
			continue
		}
		tokens := estimateTextTokens(message.Content)
		if used+tokens <= compressionToolTokenBudget {
			used += tokens
			continue
		}
		path, err := s.saveCompressionArtifact(conversationID, message.Content)
		if err != nil {
			return nil, err
		}
		tail := lastLines(message.Content, compressionToolTailLines, compressionToolTailBytes)
		message.Content = fmt.Sprintf(
			"[Older tool output truncated during context compression. Full output: %s; original size: %d bytes; showing the last %d lines.]\n\n%s",
			path, len(message.Content), compressionToolTailLines, tail,
		)
		used += estimateTextTokens(message.Content)
	}
	return result, nil
}

func (s *MemoryStore) saveCompressionArtifact(conversationID, content string) (string, error) {
	digest := sha256.Sum256([]byte(content))
	name := hex.EncodeToString(digest[:]) + ".log"
	relative := filepath.Join(".agentland", "logs", "compression", conversationID, name)
	dir := filepath.Join(s.root, "logs", "compression", conversationID)
	path := filepath.Join(dir, name)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create compression artifact directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return filepath.ToSlash(relative), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect compression artifact: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tool-output-*.log")
	if err != nil {
		return "", fmt.Errorf("create compression artifact: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("protect compression artifact: %w", err)
	}
	if _, err := io.WriteString(tmp, content); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write compression artifact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("sync compression artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close compression artifact: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("install compression artifact: %w", err)
	}
	return filepath.ToSlash(relative), nil
}

func lastLines(content string, count, byteLimit int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return utf8Suffix(strings.Join(lines, "\n"), byteLimit)
}

func utf8Suffix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func replaceContextMessages(entries []contextMessage, messages []*schema.Message) ([]contextMessage, int, int) {
	result := make([]contextMessage, len(entries))
	for index, entry := range entries {
		entry.message = messages[index]
		entry.tokens = estimateMessageTokens(messages[index])
		entry.bytes = messageSize(messages[index])
		result[index] = entry
	}
	tokens, bytes := contextMessageTotals(result)
	return result, tokens, bytes
}

func contextMessageTotals(entries []contextMessage) (int, int) {
	tokens, bytes := 0, 0
	for _, entry := range entries {
		tokens += entry.tokens
		bytes += entry.bytes
	}
	return tokens, bytes
}

func (s *MemoryStore) skipFailedCompression(conversationID string, currentTokens int) bool {
	s.mu.RLock()
	failedAt := s.compressionFailures[conversationID]
	s.mu.RUnlock()
	return failedAt > 0 && currentTokens*100 < failedAt*compressionRetryGrowthPercent
}

func (s *MemoryStore) recordCompressionFailure(conversationID string, tokens int) {
	s.mu.Lock()
	s.compressionFailures[conversationID] = tokens
	s.mu.Unlock()
}

func (s *MemoryStore) clearCompressionFailure(conversationID string) {
	s.mu.Lock()
	delete(s.compressionFailures, conversationID)
	s.mu.Unlock()
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
