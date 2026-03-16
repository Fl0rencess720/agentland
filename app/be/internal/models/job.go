package models

import (
	"encoding/json"
	"time"
)

type JobStatusResp struct {
	JobID    string   `json:"job_id"`
	Type     string   `json:"type"`
	Status   string   `json:"status"`
	Progress int      `json:"progress"`
	Logs     []string `json:"logs"`
	Result   any      `json:"result"`
}

type Job struct {
	ID               string
	OwnerID          string
	ProjectID        string
	Type             string
	Status           string
	Progress         int
	Logs             []string
	Result           any
	RequestPayload   any
	GatewaySessionID string
	AgentSessionID   string
	WorkspacePath    string
	ErrorMessage     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
}

type GenerationRequestPayload struct {
	Prompt      string          `json:"prompt"`
	Attachments []AttachmentRef `json:"attachments,omitempty"`
	Deep        bool            `json:"deep,omitempty"`
}

type CreateJobInput struct {
	ID               string
	OwnerID          string
	ProjectID        string
	Type             string
	Status           string
	Progress         int
	Logs             []string
	Result           any
	RequestPayload   any
	GatewaySessionID string
	AgentSessionID   string
	WorkspacePath    string
	ErrorMessage     string
	Now              time.Time
}

type UpdateJobInput struct {
	JobID            string
	Status           string
	Progress         int
	Logs             []string
	Result           any
	GatewaySessionID string
	AgentSessionID   string
	WorkspacePath    string
	ErrorMessage     string
	StartedAt        *time.Time
	CompletedAt      *time.Time
	UpdatedAt        time.Time
}

type AgentSessionInfo struct {
	GatewaySessionID string
}

type AgentChatStreamReq struct {
	Message       string `json:"message"`
	Deep          bool   `json:"deep,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	ProjectName   string `json:"project_name,omitempty"`
	Iterations    int    `json:"iterations,omitempty"`
}

type AgentSSEEvent struct {
	Event string
	Data  json.RawMessage
}
