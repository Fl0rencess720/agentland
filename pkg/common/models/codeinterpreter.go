package models

// CreateContextReq 对应 POST /contexts 的请求体
type CreateContextReq struct {
	Language string `json:"language" jsonschema:"Execution language, supported values: python, shell"`
	CWD      string `json:"cwd,omitempty" jsonschema:"Working directory inside sandbox, defaults to /workspace"`
}

// CreateContextResp 创建上下文接口响应体
type CreateContextResp struct {
	ContextID string `json:"context_id" jsonschema:"Created context ID"`
	Language  string `json:"language" jsonschema:"Resolved execution language"`
	CWD       string `json:"cwd" jsonschema:"Resolved working directory"`
	State     string `json:"state" jsonschema:"Context lifecycle state"`
	CreatedAt string `json:"created_at" jsonschema:"Context creation time in RFC3339 format"`
}

// ExecuteContextReq 对应 POST /contexts/{contextId}/execute 的请求体
type ExecuteContextReq struct {
	Code      string `json:"code" jsonschema:"Code snippet to execute"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"Execution timeout in milliseconds, valid range is 100-300000"`
}

// ExecuteContextResp 上下文执行接口响应体
type ExecuteContextResp struct {
	ContextID      string `json:"context_id" jsonschema:"Context ID where execution runs"`
	ExecutionCount int64  `json:"execution_count" jsonschema:"Monotonic execution counter in the context"`
	ExitCode       int32  `json:"exit_code" jsonschema:"Process-like exit code, 0 means success"`
	Stdout         string `json:"stdout" jsonschema:"Captured standard output"`
	Stderr         string `json:"stderr" jsonschema:"Captured standard error"`
	DurationMs     int64  `json:"duration_ms" jsonschema:"Execution duration in milliseconds"`
}

// DeleteContextResp 删除上下文接口响应体
type DeleteContextResp struct {
	ContextID string `json:"context_id" jsonschema:"Deleted context ID"`
}
