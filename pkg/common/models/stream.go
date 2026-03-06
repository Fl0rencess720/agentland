package models

// ExecuteStreamEvent is one event frame in SSE streaming execution.
// It is intentionally small and generic so that clients can incrementally render output.
type ExecuteStreamEvent struct {
	// Type is the event kind: init, stdout, stderr, count, status, execution_complete, error, ping.
	Type string `json:"type"`

	// Timestamp is milliseconds since epoch.
	Timestamp int64 `json:"timestamp,omitempty"`

	ContextID   string `json:"context_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`

	// Text carries stdout/stderr/status payload.
	Text string `json:"text,omitempty"`

	ExecutionCount int64 `json:"execution_count,omitempty"`

	// ExecutionTime is only set for "execution_complete" events (milliseconds).
	ExecutionTime int64 `json:"execution_time,omitempty"`

	// ExitCode is only set for "execution_complete" events.
	ExitCode int32 `json:"exit_code,omitempty"`

	// Result is deprecated; do not rely on it being populated.
	Result *ExecuteContextResp `json:"result,omitempty"`

	// Error is only set for "error" events.
	Error string `json:"error,omitempty"`
}
