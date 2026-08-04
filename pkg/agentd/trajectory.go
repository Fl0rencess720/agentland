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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const (
	EventTrajectoryRecord = "trajectory.record"

	TrajectoryRunStarted  = "run.started"
	TrajectoryModelInput  = "model.input"
	TrajectoryModelOutput = "model.output"
	TrajectoryToolResult  = "tool.result"
	TrajectoryRunFinished = "run.finished"

	maxTrajectoryLineBytes = 16 << 20
	maxReplayRequestBytes  = 32 << 20
	maxTraceAttributeBytes = 256 << 10
)

type TrajectoryRecord struct {
	Version        int             `json:"version"`
	RunID          string          `json:"run_id"`
	ConversationID string          `json:"conversation_id"`
	Sequence       int64           `json:"sequence"`
	Type           string          `json:"type"`
	Step           int             `json:"step,omitempty"`
	Timestamp      string          `json:"timestamp"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	PreviousHash   string          `json:"previous_hash,omitempty"`
	Hash           string          `json:"hash"`
}

type trajectoryRunStarted struct {
	Message        string `json:"message"`
	Model          string `json:"model,omitempty"`
	SystemPrompt   string `json:"system_prompt"`
	PromptHash     string `json:"prompt_hash"`
	ToolSchemaHash string `json:"tool_schema_hash"`
	CaptureVersion string `json:"capture_version"`
}

type trajectoryModelInput struct {
	Messages []*schema.Message `json:"messages"`
}

type trajectoryModelOutput struct {
	Message *schema.Message `json:"message"`
}

type trajectoryToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
}

type trajectoryRunFinished struct {
	Status  string `json:"status"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

type TrajectoryStore struct {
	root   string
	mu     sync.Mutex
	states map[string]trajectoryState
}

type trajectoryState struct {
	sequence int64
	hash     string
}

func NewTrajectoryStore(workspaceRoot string) *TrajectoryStore {
	return &TrajectoryStore{root: filepath.Join(workspaceRoot, ".agentland", "trajectories"), states: make(map[string]trajectoryState)}
}

func (s *TrajectoryStore) Append(runID, conversationID, recordType string, step int, payload any) (*TrajectoryRecord, error) {
	if !validID(runID) || !validID(conversationID) {
		return nil, errors.New("invalid trajectory identifier")
	}
	payloadData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal trajectory payload: %w", err)
	}
	if len(payloadData) > maxTrajectoryLineBytes/2 {
		return nil, fmt.Errorf("trajectory payload exceeds %d bytes", maxTrajectoryLineBytes/2)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("create trajectory directory: %w", err)
	}
	path := filepath.Join(s.root, runID+".jsonl")
	state, exists := s.states[runID]
	if !exists {
		records, err := readTrajectoryFile(path)
		if err != nil {
			return nil, err
		}
		if len(records) != 0 {
			state = trajectoryState{sequence: records[len(records)-1].Sequence, hash: records[len(records)-1].Hash}
		}
	}
	record := &TrajectoryRecord{
		Version: 1, RunID: runID, ConversationID: conversationID,
		Sequence: state.sequence + 1, Type: recordType, Step: step,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Payload: payloadData,
	}
	record.PreviousHash = state.hash
	record.Hash, err = trajectoryHash(record)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal trajectory record: %w", err)
	}
	if len(data) > maxTrajectoryLineBytes {
		return nil, fmt.Errorf("trajectory record exceeds %d bytes", maxTrajectoryLineBytes)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open trajectory: %w", err)
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return nil, fmt.Errorf("append trajectory: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close trajectory: %w", closeErr)
	}
	s.states[runID] = trajectoryState{sequence: record.Sequence, hash: record.Hash}
	return record, nil
}

func (s *TrajectoryStore) Records(runID string) ([]TrajectoryRecord, error) {
	if !validID(runID) {
		return nil, errors.New("invalid run_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return readTrajectoryFile(filepath.Join(s.root, runID+".jsonl"))
}

func readTrajectoryFile(path string) ([]TrajectoryRecord, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open trajectory: %w", err)
	}
	defer file.Close()

	records := make([]TrajectoryRecord, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxTrajectoryLineBytes)
	previousHash := ""
	for scanner.Scan() {
		var record TrajectoryRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("decode trajectory record %d: %w", len(records)+1, err)
		}
		if record.Version != 1 || record.Sequence != int64(len(records)+1) || record.PreviousHash != previousHash {
			return nil, fmt.Errorf("trajectory chain is invalid at sequence %d", record.Sequence)
		}
		hash, err := trajectoryHash(&record)
		if err != nil {
			return nil, err
		}
		if hash != record.Hash {
			return nil, fmt.Errorf("trajectory hash is invalid at sequence %d", record.Sequence)
		}
		previousHash = record.Hash
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read trajectory: %w", err)
	}
	return records, nil
}

func trajectoryHash(record *TrajectoryRecord) (string, error) {
	copy := *record
	copy.Hash = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("marshal trajectory hash input: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

type DecisionReplayRequest struct {
	Records []TrajectoryRecord `json:"records"`
}

type ReplayToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type DecisionReplayStep struct {
	Step           int              `json:"step"`
	Matched        bool             `json:"matched"`
	Expected       []ReplayToolCall `json:"expected"`
	Actual         []ReplayToolCall `json:"actual"`
	ContentChanged bool             `json:"content_changed"`
}

type DecisionReplayReport struct {
	Mode         string               `json:"mode"`
	Status       string               `json:"status"`
	TotalSteps   int                  `json:"total_steps"`
	MatchedSteps int                  `json:"matched_steps"`
	Score        float64              `json:"score"`
	Steps        []DecisionReplayStep `json:"steps"`
	Output       string               `json:"output,omitempty"`
	Error        string               `json:"error,omitempty"`
}

func replayDecisions(ctx context.Context, chatModel model.ToolCallingChatModel, currentPrompt string, records []TrajectoryRecord) (*DecisionReplayReport, error) {
	inputs := make(map[int][]*schema.Message)
	expected := make(map[int]*schema.Message)
	order := make([]int, 0)
	sourcePrompt := replaySourcePrompt(records)
	for _, record := range records {
		switch record.Type {
		case TrajectoryModelInput:
			var payload trajectoryModelInput
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode model input at sequence %d: %w", record.Sequence, err)
			}
			if _, exists := inputs[record.Step]; exists {
				return nil, fmt.Errorf("duplicate model input for step %d", record.Step)
			}
			inputs[record.Step] = payload.Messages
			order = append(order, record.Step)
		case TrajectoryModelOutput:
			var payload trajectoryModelOutput
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode model output at sequence %d: %w", record.Sequence, err)
			}
			expected[record.Step] = payload.Message
		}
	}
	if len(order) == 0 {
		return nil, errors.New("trajectory has no model steps")
	}

	report := &DecisionReplayReport{Mode: "decision", Status: "completed", TotalSteps: len(order), Steps: make([]DecisionReplayStep, 0, len(order))}
	tracer := otel.Tracer("agentland/agentd")
	for _, step := range order {
		expectedMessage := expected[step]
		if expectedMessage == nil {
			return nil, fmt.Errorf("trajectory is missing model output for step %d", step)
		}
		stepCtx, span := tracer.Start(ctx, "agent.replay.decision")
		span.SetAttributes(
			attribute.Int("agent.step", step),
			attribute.String("gen_ai.operation.name", "evaluate"),
			attribute.String("langfuse.observation.type", "evaluator"),
			attribute.String("langfuse.observation.input", traceJSON(map[string]any{"messages": inputs[step], "expected": expectedMessage})),
		)
		actual, err := generateReplayResponse(stepCtx, chatModel, applyReplayPrompt(inputs[step], sourcePrompt, currentPrompt))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return nil, fmt.Errorf("replay model step %d: %w", step, err)
		}
		result := DecisionReplayStep{
			Step: step, Expected: replayToolCalls(expectedMessage.ToolCalls), Actual: replayToolCalls(actual.ToolCalls),
			ContentChanged: actual.Content != expectedMessage.Content,
		}
		result.Matched = toolCallsEqual(result.Expected, result.Actual)
		if result.Matched {
			report.MatchedSteps++
		}
		span.SetAttributes(
			attribute.Bool("agent.replay.matched", result.Matched),
			attribute.String("langfuse.observation.output", traceJSON(result)),
		)
		span.End()
		report.Steps = append(report.Steps, result)
	}
	report.Score = float64(report.MatchedSteps) / float64(report.TotalSteps)
	return report, nil
}

func replayLive(ctx context.Context, agent *Agent, records []TrajectoryRecord) (*DecisionReplayReport, error) {
	inputs := make(map[int][]*schema.Message)
	expected := make(map[int]*schema.Message)
	for _, record := range records {
		switch record.Type {
		case TrajectoryModelInput:
			var payload trajectoryModelInput
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode model input at sequence %d: %w", record.Sequence, err)
			}
			inputs[record.Step] = payload.Messages
		case TrajectoryModelOutput:
			var payload trajectoryModelOutput
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode model output at sequence %d: %w", record.Sequence, err)
			}
			expected[record.Step] = payload.Message
		}
	}
	messages := applyReplayPrompt(inputs[1], replaySourcePrompt(records), agent.prompt)
	if len(messages) == 0 {
		return nil, errors.New("trajectory has no first model input")
	}
	report := &DecisionReplayReport{Mode: "live", Status: "running", Steps: make([]DecisionReplayStep, 0)}
	for step := 1; ; step++ {
		actual, err := generateReplayResponse(ctx, agent.model, messages)
		if err != nil {
			report.Status, report.Error = "failed", err.Error()
			return report, err
		}
		expectedMessage := expected[step]
		result := DecisionReplayStep{Step: step, Actual: replayToolCalls(actual.ToolCalls)}
		if expectedMessage != nil {
			result.Expected = replayToolCalls(expectedMessage.ToolCalls)
			result.ContentChanged = actual.Content != expectedMessage.Content
			result.Matched = toolCallsEqual(result.Expected, result.Actual)
		}
		if result.Matched {
			report.MatchedSteps++
		}
		report.Steps = append(report.Steps, result)
		report.TotalSteps++
		messages = append(messages, actual)
		if len(actual.ToolCalls) == 0 {
			report.Status, report.Output = "completed", actual.Content
			break
		}
		for _, call := range actual.ToolCalls {
			output, toolErr := agent.invokeTool(ctx, call)
			if toolErr != nil {
				output = "tool error: " + toolErr.Error()
			}
			messages = append(messages, schema.ToolMessage(boundToolOutput(output), call.ID, schema.WithToolName(call.Function.Name)))
		}
	}
	report.TotalSteps = max(report.TotalSteps, len(expected))
	if report.TotalSteps != 0 {
		report.Score = float64(report.MatchedSteps) / float64(report.TotalSteps)
	}
	return report, nil
}

func cloneMessages(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		copy := *message
		copy.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
		result = append(result, &copy)
	}
	return result
}

func replaySourcePrompt(records []TrajectoryRecord) string {
	for _, record := range records {
		if record.Type != TrajectoryRunStarted {
			continue
		}
		var payload trajectoryRunStarted
		if json.Unmarshal(record.Payload, &payload) == nil {
			return payload.SystemPrompt
		}
	}
	return ""
}

func applyReplayPrompt(messages []*schema.Message, sourcePrompt, currentPrompt string) []*schema.Message {
	result := cloneMessages(messages)
	if currentPrompt == "" {
		return result
	}
	for _, message := range result {
		if message.Role != schema.System {
			continue
		}
		suffix := ""
		if sourcePrompt != "" && strings.HasPrefix(message.Content, sourcePrompt) {
			suffix = strings.TrimPrefix(message.Content, sourcePrompt)
		}
		message.Content = currentPrompt + suffix
		break
	}
	return result
}

func generateReplayResponse(ctx context.Context, chatModel model.ToolCallingChatModel, messages []*schema.Message) (response *schema.Message, resultErr error) {
	ctx, span := otel.Tracer("agentland/agentd").Start(ctx, "model.replay.generate")
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.input.messages", traceJSON(messages)),
		attribute.String("langfuse.observation.type", "generation"),
		attribute.String("langfuse.observation.input", traceJSON(messages)),
	)
	defer func() {
		if resultErr != nil {
			span.RecordError(resultErr)
			span.SetStatus(codes.Error, resultErr.Error())
		}
		span.End()
	}()
	stream, err := chatModel.Stream(ctx, messages)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	chunks := make([]*schema.Message, 0)
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, message)
	}
	if len(chunks) == 0 {
		return nil, errors.New("model returned an empty stream")
	}
	response, err = schema.ConcatMessages(chunks)
	if err == nil {
		span.SetAttributes(attribute.String("gen_ai.output.messages", traceJSON(response)))
		span.SetAttributes(attribute.String("langfuse.observation.output", traceJSON(response)))
		if response.ResponseMeta != nil && response.ResponseMeta.Usage != nil {
			span.SetAttributes(
				attribute.Int("gen_ai.usage.input_tokens", response.ResponseMeta.Usage.PromptTokens),
				attribute.Int("gen_ai.usage.output_tokens", response.ResponseMeta.Usage.CompletionTokens),
			)
		}
	}
	return response, err
}

func replayToolCalls(calls []schema.ToolCall) []ReplayToolCall {
	result := make([]ReplayToolCall, 0, len(calls))
	for _, call := range calls {
		result = append(result, ReplayToolCall{Name: call.Function.Name, Arguments: canonicalJSON(call.Function.Arguments)})
	}
	return result
}

func canonicalJSON(value string) string {
	var decoded any
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return strings.TrimSpace(value)
	}
	data, err := json.Marshal(decoded)
	if err != nil {
		return strings.TrimSpace(value)
	}
	return string(data)
}

func toolCallsEqual(left, right []ReplayToolCall) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func stableHash(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func traceJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{"error":"trace serialization failed"}`
	}
	return traceString(string(data))
}

func traceString(value string) string {
	if len(value) <= maxTraceAttributeBytes {
		return value
	}
	return strings.ToValidUTF8(value[:maxTraceAttributeBytes], "") + "...[truncated]"
}

func parseReplayRecords(reader io.Reader) ([]TrajectoryRecord, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, maxReplayRequestBytes+1))
	var request DecisionReplayRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	if len(request.Records) == 0 {
		return nil, errors.New("records are required")
	}
	previousHash := ""
	for index := range request.Records {
		record := &request.Records[index]
		if record.Version != 1 || record.Sequence != int64(index+1) || record.PreviousHash != previousHash {
			return nil, fmt.Errorf("record %s has an invalid chain", strconv.Itoa(index))
		}
		hash, err := trajectoryHash(record)
		if err != nil || hash != record.Hash {
			return nil, fmt.Errorf("record %s has an invalid hash", strconv.Itoa(index))
		}
		previousHash = record.Hash
	}
	return request.Records, nil
}
