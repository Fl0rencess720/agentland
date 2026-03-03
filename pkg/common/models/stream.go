package models

// ExecuteStreamEvent is one event frame in SSE streaming execution.
// It is intentionally small and generic so that clients can incrementally render output.
type ExecuteStreamEvent struct {
	// Type is the event kind: init, stdout, stderr, count, status, complete, error, ping.
	Type string `json:"type"`

	// Timestamp is milliseconds since epoch.
	Timestamp int64 `json:"timestamp,omitempty"`

	ContextID string `json:"context_id,omitempty"`

	// Text carries stdout/stderr/status payload.
	Text string `json:"text,omitempty"`

	ExecutionCount int64 `json:"execution_count,omitempty"`

	// Result is only set for "complete" events.
	Result *ExecuteContextResp `json:"result,omitempty"`

	// Error is only set for "error" events.
	Error string `json:"error,omitempty"`
}

