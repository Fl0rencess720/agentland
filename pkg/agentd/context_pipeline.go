package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const (
	contextStageToolBudget = "tool_result_budget"
	contextStageSnip       = "snip"
	contextStageMicro      = "microcompact_age_size"
	contextStageCollapse   = "context_collapse"
	contextStageAuto       = "auto_compact"
)

var progressiveContextStageOrder = []string{
	contextStageToolBudget,
	contextStageSnip,
	contextStageMicro,
	contextStageCollapse,
	contextStageAuto,
}

type contextStageReport struct {
	Name         string
	Triggered    bool
	TokensBefore int
	TokensAfter  int
}

func (s *MemoryStore) progressiveContext(
	ctx context.Context,
	conversationID string,
	chatModel model.BaseChatModel,
	summary *conversationSummary,
	collapse *conversationCollapse,
	entries []contextMessage,
	historyBudget int,
) (*conversationSummary, *conversationCollapse, []contextMessage, bool, bool, error) {
	result, updatedSummary, updatedCollapse, summaryChanged, collapseChanged, _, err := s.runProgressiveContext(
		ctx, conversationID, chatModel, summary, collapse, entries, historyBudget,
	)
	return updatedSummary, updatedCollapse, result, summaryChanged, collapseChanged, err
}

func (s *MemoryStore) runProgressiveContext(
	ctx context.Context,
	conversationID string,
	chatModel model.BaseChatModel,
	summary *conversationSummary,
	collapse *conversationCollapse,
	entries []contextMessage,
	historyBudget int,
) ([]contextMessage, *conversationSummary, *conversationCollapse, bool, bool, []contextStageReport, error) {
	reports := make([]contextStageReport, 0, len(progressiveContextStageOrder))
	ctx, span := otel.Tracer("agentland/agentd").Start(ctx, "memory.context_pipeline")
	defer func() {
		span.SetAttributes(attribute.StringSlice("agent.memory.stage_order", progressiveContextStageOrder))
		for _, report := range reports {
			prefix := "agent.memory.stage." + report.Name
			span.SetAttributes(
				attribute.Bool(prefix+".triggered", report.Triggered),
				attribute.Int(prefix+".tokens_before", report.TokensBefore),
				attribute.Int(prefix+".tokens_after", report.TokensAfter),
				attribute.Int(prefix+".tokens_released", max(0, report.TokensBefore-report.TokensAfter)),
			)
		}
		span.End()
	}()
	view := cloneContextEntries(entries)

	before := contextEntryTokens(view)
	var err error
	view, err = s.applyToolResultBudget(conversationID, view)
	if err != nil {
		return nil, summary, collapse, false, false, reports, err
	}
	reports = appendStageReport(reports, contextStageToolBudget, before, view)

	before = contextEntryTokens(view)
	view = snipOldReasoning(view, historyBudget)
	reports = appendStageReport(reports, contextStageSnip, before, view)

	before = contextEntryTokens(view)
	view, err = s.microcompactOldToolResults(conversationID, view, historyBudget)
	if err != nil {
		return nil, summary, collapse, false, false, reports, err
	}
	reports = appendStageReport(reports, contextStageMicro, before, view)

	preCollapse := view
	before = contextEntryTokens(view)
	view, updatedCollapse, collapseChanged, err := s.collapseOldContext(
		ctx, conversationID, chatModel, summary, collapse, view, historyBudget,
	)
	if err != nil {
		return nil, summary, collapse, false, false, reports, err
	}
	reports = appendStageReport(reports, contextStageCollapse, before, view)
	if collapseChanged {
		reports[len(reports)-1].Triggered = true
	}

	before = contextEntryTokens(view)
	updatedSummary := summary
	summaryChanged := false
	if contextExceedsBudget(view, historyBudget) {
		rawTokens, rawBytes := contextMessageTotals(preCollapse)
		var compacted []contextMessage
		updatedSummary, compacted, _, _, summaryChanged, err = s.autoCompactContext(
			ctx, conversationID, chatModel, summary, preCollapse, rawTokens, rawBytes, historyBudget,
		)
		if err != nil {
			return nil, summary, collapse, false, false, reports, err
		}
		if summaryChanged {
			view = compacted
			if updatedCollapse != nil {
				updatedCollapse = nil
				collapseChanged = true
			}
		} else if contextEntryTokens(compacted) < contextEntryTokens(view) {
			view = compacted
		}
	}
	reports = appendStageReport(reports, contextStageAuto, before, view)
	if summaryChanged {
		reports[len(reports)-1].Triggered = true
	}
	return view, updatedSummary, updatedCollapse, summaryChanged, collapseChanged, reports, nil
}

func appendStageReport(reports []contextStageReport, name string, before int, entries []contextMessage) []contextStageReport {
	after := contextEntryTokens(entries)
	return append(reports, contextStageReport{Name: name, Triggered: after < before, TokensBefore: before, TokensAfter: after})
}

func cloneContextEntries(entries []contextMessage) []contextMessage {
	result := make([]contextMessage, len(entries))
	for index, entry := range entries {
		messages := cloneMessages([]*schema.Message{entry.message})
		if len(messages) == 1 {
			entry.message = messages[0]
		}
		entry.tokens = estimateMessageTokens(entry.message)
		entry.bytes = messageSize(entry.message)
		result[index] = entry
	}
	return result
}

func contextEntryTokens(entries []contextMessage) int {
	tokens, _ := contextMessageTotals(entries)
	return tokens
}

func contextExceedsBudget(entries []contextMessage, historyBudget int) bool {
	_, bytes := contextMessageTotals(entries)
	return contextEntryTokens(entries) > historyBudget || bytes > maxContextHistoryChars || len(entries) > maxContextMessages
}

func (s *MemoryStore) applyToolResultBudget(conversationID string, entries []contextMessage) ([]contextMessage, error) {
	result := cloneContextEntries(entries)
	messages, err := s.boundToolResultMessages(conversationID, contextMessages(result))
	if err != nil {
		return nil, err
	}
	result, _, _ = replaceContextMessages(result, messages)
	return result, nil
}

func (s *MemoryStore) boundToolResultMessages(conversationID string, messages []*schema.Message) ([]*schema.Message, error) {
	result := cloneMessages(messages)
	minimumBefore := make([]int, len(result))
	minimumTotal := 0
	for index, message := range result {
		minimumBefore[index] = minimumTotal
		if message == nil || message.Role != schema.Tool || message.Content == "" {
			continue
		}
		path := compressionArtifactRelativePath(conversationID, message.Content)
		if existing := compressionReferencePath(message.Content); existing != "" {
			path = existing
		}
		minimumTotal += min(estimateTextTokens(message.Content), estimateTextTokens("Full output: "+path))
	}

	used := 0
	for index := len(result) - 1; index >= 0; index-- {
		message := result[index]
		if message == nil || message.Role != schema.Tool || message.Content == "" {
			continue
		}
		tokens := estimateTextTokens(message.Content)
		reserveOlder := minimumBefore[index]
		if minimumTotal > compressionToolTokenBudget {
			reserveOlder = 0
		}
		allowed := min(compressionSingleToolTokens, max(0, compressionToolTokenBudget-used-reserveOlder))
		if tokens <= allowed {
			used += tokens
			continue
		}
		original := message.Content
		path := compressionReferencePath(original)
		if path == "" {
			var err error
			path, err = s.saveCompressionArtifact(conversationID, message.Content)
			if err != nil {
				return nil, err
			}
		}
		message.Content = fitToolOutputReference(path, original, allowed)
		used += estimateTextTokens(message.Content)
	}
	return result, nil
}

func snipOldReasoning(entries []contextMessage, historyBudget int) []contextMessage {
	result := cloneContextEntries(entries)
	if contextEntryTokens(result)*100 <= historyBudget*snipTriggerPercent {
		return result
	}
	messages := contextMessages(result)
	recentStart := compressionSplitPoint(messages, compressionPreservePercent)
	recentStart = min(recentStart, max(0, len(result)-compressionRecentMessages))
	for index := 0; index < recentStart; index++ {
		message := result[index].message
		if message == nil || message.Role != schema.Assistant || message.ReasoningContent == "" {
			continue
		}
		message.ReasoningContent = ""
		result[index].tokens = estimateMessageTokens(message)
		result[index].bytes = messageSize(message)
	}
	return result
}

func (s *MemoryStore) microcompactOldToolResults(conversationID string, entries []contextMessage, historyBudget int) ([]contextMessage, error) {
	result := cloneContextEntries(entries)
	if contextEntryTokens(result)*100 <= historyBudget*microcompactTriggerPercent {
		return result, nil
	}
	recentStart := compressionSplitPoint(contextMessages(result), compressionPreservePercent)
	recentStart = min(recentStart, max(0, len(result)-compressionRecentMessages))
	seen := make(map[string]struct{})
	for index := len(result) - 1; index >= 0; index-- {
		message := result[index].message
		if message == nil || message.Role != schema.Tool || message.Content == "" {
			continue
		}
		digest := sha256.Sum256([]byte(message.Content))
		key := hex.EncodeToString(digest[:])
		_, duplicate := seen[key]
		seen[key] = struct{}{}
		duplicate = duplicate && index < recentStart
		oldAndLarge := index < recentStart && estimateTextTokens(message.Content) >= microcompactToolTokens && refetchableTool(message.ToolName)
		if (!duplicate && !oldAndLarge) || isCompressionReference(message.Content) {
			continue
		}
		path, err := s.saveCompressionArtifact(conversationID, message.Content)
		if err != nil {
			return nil, err
		}
		reason := "deterministic age/size microcompaction"
		if duplicate {
			reason = "duplicate-result microcompaction"
		}
		message.Content = fmt.Sprintf("[Older tool output omitted by %s. Full output: %s; original size: %d bytes.]", reason, path, len(message.Content))
		result[index].tokens = estimateMessageTokens(message)
		result[index].bytes = messageSize(message)
	}
	return result, nil
}

func (s *MemoryStore) collapseOldContext(
	ctx context.Context,
	conversationID string,
	chatModel model.BaseChatModel,
	summary *conversationSummary,
	stored *conversationCollapse,
	entries []contextMessage,
	historyBudget int,
) ([]contextMessage, *conversationCollapse, bool, error) {
	if view, ok := reuseCollapse(summary, stored, entries); ok {
		return view, stored, false, nil
	}
	staleStored := stored != nil
	if len(entries) < contextCollapseMinMessages || contextEntryTokens(entries)*100 <= historyBudget*contextCollapseTriggerPercent {
		return entries, nil, staleStored, nil
	}
	if s.skipFailedCompression(conversationID, contextEntryTokens(entries)) {
		return entries, nil, staleStored, nil
	}

	messages := contextMessages(entries)
	split := compressionSplitPoint(messages, contextCollapsePreservePercent)
	if split < 4 || len(entries)-split < 4 {
		return entries, nil, staleStored, nil
	}
	content, err := generateContextCollapse(ctx, chatModel, s.compressionModel, summary, messages[:split])
	if err != nil {
		return entries, nil, staleStored, nil
	}
	next := &conversationCollapse{
		Version:   1,
		UpTo:      entries[split-1].number,
		Offset:    entries[split-1].nextOffset,
		Content:   content,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if summary != nil {
		next.BaseUpTo = summary.UpTo
		next.BaseOffset = summary.Offset
	}
	s.clearCompressionFailure(conversationID)
	return collapsedContextEntries(next, entries[split:]), next, true, nil
}

func reuseCollapse(summary *conversationSummary, collapse *conversationCollapse, entries []contextMessage) ([]contextMessage, bool) {
	if collapse == nil || strings.TrimSpace(collapse.Content) == "" {
		return nil, false
	}
	baseUpTo, baseOffset := 0, int64(0)
	if summary != nil {
		baseUpTo, baseOffset = summary.UpTo, summary.Offset
	}
	if collapse.BaseUpTo != baseUpTo || collapse.BaseOffset != baseOffset || collapse.UpTo <= baseUpTo || collapse.Offset <= baseOffset {
		return nil, false
	}
	for index, entry := range entries {
		if entry.number == collapse.UpTo && entry.nextOffset == collapse.Offset {
			if index+1 >= len(entries) || !isCompressionBoundary(entries[index+1].message) {
				return nil, false
			}
			return collapsedContextEntries(collapse, entries[index+1:]), true
		}
	}
	return nil, false
}

func collapsedContextEntries(collapse *conversationCollapse, recent []contextMessage) []contextMessage {
	result := make([]contextMessage, 0, len(recent)+2)
	user := schema.UserMessage("Earlier conversation context fold:\n" + collapse.Content)
	assistant := schema.AssistantMessage("Understood. I will use this context with the recent history.", nil)
	result = append(result,
		contextMessage{message: user, number: collapse.UpTo, nextOffset: collapse.Offset, tokens: estimateMessageTokens(user), bytes: messageSize(user)},
		contextMessage{message: assistant, number: collapse.UpTo, nextOffset: collapse.Offset, tokens: estimateMessageTokens(assistant), bytes: messageSize(assistant)},
	)
	return append(result, recent...)
}

func generateContextCollapse(
	ctx context.Context,
	chatModel model.BaseChatModel,
	compressionModel string,
	summary *conversationSummary,
	messages []*schema.Message,
) (string, error) {
	prompt := "Fold the supplied older history while preserving everything needed to continue the coding task."
	if summary != nil && strings.TrimSpace(summary.Content) != "" {
		prompt += "\nA persistent state snapshot already precedes this history:\n" + summary.Content
	}
	input := make([]*schema.Message, 0, len(messages)+2)
	input = append(input, schema.SystemMessage(contextCollapseSystemPrompt))
	input = append(input, cloneMessages(messages)...)
	input = append(input, schema.UserMessage(prompt))
	options := []model.Option{model.WithTemperature(0), model.WithMaxTokens(compressionMaxOutputTokens)}
	if compressionModel != "" {
		options = append(options, model.WithModel(compressionModel))
	}
	response, err := chatModel.Generate(ctx, input, options...)
	if err != nil {
		return "", fmt.Errorf("generate context collapse: %w", err)
	}
	content := normalizeTaggedBlock(response.Content, "context_collapse")
	if content == "" {
		return "", fmt.Errorf("generate context collapse: empty response")
	}
	return utf8Prefix(content, maxSummaryContentBytes), nil
}

func normalizeTaggedBlock(content, tag string) string {
	content = strings.TrimSpace(strings.ToValidUTF8(content, "\uFFFD"))
	if content == "" {
		return ""
	}
	open, close := "<"+tag+">", "</"+tag+">"
	if start := strings.Index(content, open); start >= 0 {
		if end := strings.Index(content[start:], close); end >= 0 {
			return content[start : start+end+len(close)]
		}
	}
	return open + "\n" + content + "\n" + close
}

func contextMessages(entries []contextMessage) []*schema.Message {
	messages := make([]*schema.Message, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, entry.message)
	}
	return messages
}

func refetchableTool(name string) bool {
	switch name {
	case "read_file", "list_files", "grep", "read_skill":
		return true
	default:
		return strings.HasSuffix(name, "_read") || strings.HasSuffix(name, "_get") || strings.HasSuffix(name, "_list") || strings.HasSuffix(name, "_search")
	}
}

func isCompressionReference(content string) bool {
	return strings.Contains(content, "Full output: .agentland/logs/compression/")
}

func compressionReferencePath(content string) string {
	const marker = "Full output: "
	start := strings.Index(content, marker)
	if start < 0 {
		return ""
	}
	value := content[start+len(marker):]
	if end := strings.IndexAny(value, ";]\n"); end >= 0 {
		value = value[:end]
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, ".agentland/logs/compression/") {
		return ""
	}
	return value
}

func fitToolOutputReference(path, content string, tokenLimit int) string {
	if tokenLimit <= 0 {
		return ""
	}
	originalBytes := len(content)
	content = strings.ToValidUTF8(content, "\uFFFD")
	base := fmt.Sprintf("[Tool output archived. Full output: %s; original size: %d bytes.]", path, originalBytes)
	if estimateTextTokens(base) > tokenLimit {
		base = "Full output: " + path
	}
	if estimateTextTokens(base) > tokenLimit {
		return ""
	}

	best := base
	high := min(len(content), compressionToolTailBytes)
	for low := 1; low <= high; {
		middle := low + (high-low)/2
		candidate := base + "\n\n" + lastLines(content, compressionToolTailLines, middle)
		if estimateTextTokens(candidate) <= tokenLimit {
			best = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}
