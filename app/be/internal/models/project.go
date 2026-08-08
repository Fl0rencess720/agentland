package models

import (
	"encoding/json"
	"time"
)

const (
	MaxRunMessageBytes  = 256 << 10
	MaxFileContentBytes = 1 << 20

	RunStatusQueued    = "queued"
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusCancelled = "cancelled"

	RuntimeStatusActive      = "active"
	RuntimeStatusExpired     = "expired"
	RuntimeStatusUnavailable = "unavailable"

	PublicationStatusQueued    = "queued"
	PublicationStatusRunning   = "running"
	PublicationStatusCompleted = "completed"
	PublicationStatusFailed    = "failed"
	PublicationStatusCancelled = "cancelled"
)

type ProjectListReq struct {
	View      string `form:"view"`
	Keyword   string `form:"keyword"`
	Status    string `form:"status"`
	SortBy    string `form:"sort_by"`
	SortOrder string `form:"sort_order"`
	Page      int    `form:"page"`
	PageSize  int    `form:"page_size"`
}

type ProjectMetadata struct {
	LastViewMode string `json:"last_view_mode,omitempty"`
}

type ProjectItem struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	ThumbnailURL string `json:"thumbnail_url"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	IsShared     bool   `json:"is_shared"`
}

type Pagination struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

type ProjectListResp struct {
	Items      []ProjectItem `json:"items"`
	Pagination Pagination    `json:"pagination"`
}

type ProjectCreateReq struct {
	Name     string `json:"name" binding:"required"`
	Template string `json:"template" binding:"required"`
}

type ProjectCreateResp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type ProjectDetailResp struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Status        string           `json:"status"`
	OwnerID       string           `json:"owner_id"`
	LastOpenedAt  string           `json:"last_opened_at"`
	Metadata      *ProjectMetadata `json:"metadata,omitempty"`
	RuntimeStatus string           `json:"runtime_status"`
	ActiveRunID   *string          `json:"active_run_id"`
	LastRunID     *string          `json:"last_run_id"`
}

type ProjectUpdateReq struct {
	Name     string           `json:"name"`
	Metadata *ProjectMetadata `json:"metadata"`
}

type ProjectUpdateResp struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	UpdatedAt string           `json:"updated_at"`
	Metadata  *ProjectMetadata `json:"metadata,omitempty"`
}

type ProjectDeleteResp struct {
	Success bool `json:"success"`
}
type ProjectUsageResp struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

type Project struct {
	ID           string
	OwnerID      string
	Name         string
	Template     string
	Status       string
	ThumbnailURL string
	Metadata     ProjectMetadata
	LastOpenedAt *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

type ProjectListFilter struct {
	OwnerID, Keyword, Status, SortBy, SortOrder, View string
	Page, PageSize                                    int
}

type CreateProjectInput struct {
	ID, OwnerID, Name, Template, Status string
	Now                                 time.Time
}

type UpdateProjectInput struct {
	ProjectID, OwnerID, Name string
	Metadata                 ProjectMetadata
	Now                      time.Time
}

type RunCreateReq struct {
	Message string `json:"message" binding:"required"`
}

type RunCreateResp struct {
	RunID         string `json:"run_id"`
	UserMessageID string `json:"user_message_id"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
}

type RunResp struct {
	ID                 string  `json:"id"`
	ProjectID          string  `json:"project_id"`
	Status             string  `json:"status"`
	InputMessageID     string  `json:"input_message_id"`
	AssistantMessageID string  `json:"assistant_message_id"`
	ErrorCode          string  `json:"error_code,omitempty"`
	ErrorMessage       string  `json:"error_message,omitempty"`
	LastSequence       int64   `json:"last_sequence"`
	CancelRequestedAt  *string `json:"cancel_requested_at,omitempty"`
	CreatedAt          string  `json:"created_at"`
	StartedAt          *string `json:"started_at,omitempty"`
	CompletedAt        *string `json:"completed_at,omitempty"`
}

type RunCancelResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

const (
	ReplayModeDecision = "decision"
	ReplayModeLive     = "live"
)

type RunTrajectoryRecord struct {
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

type RunTrajectoryResp struct {
	RunID   string                `json:"run_id"`
	Records []RunTrajectoryRecord `json:"records"`
}

type ReplayRunReq struct {
	Mode string `json:"mode" binding:"required"`
}

type ReplayToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ReplayStep struct {
	Step           int              `json:"step"`
	Matched        bool             `json:"matched"`
	Expected       []ReplayToolCall `json:"expected"`
	Actual         []ReplayToolCall `json:"actual"`
	ContentChanged bool             `json:"content_changed"`
}

type ReplayRunResp struct {
	ID                string       `json:"id"`
	SourceRunID       string       `json:"source_run_id"`
	Mode              string       `json:"mode"`
	Status            string       `json:"status"`
	TotalSteps        int          `json:"total_steps"`
	MatchedSteps      int          `json:"matched_steps"`
	Score             float64      `json:"score"`
	Steps             []ReplayStep `json:"steps"`
	SourceSnapshotSHA string       `json:"source_snapshot_sha,omitempty"`
	OutputSnapshotSHA string       `json:"output_snapshot_sha,omitempty"`
	WorkspaceChanged  bool         `json:"workspace_changed,omitempty"`
	Output            string       `json:"output,omitempty"`
	Error             string       `json:"error,omitempty"`
}

type WorkspaceSnapshot struct {
	Data      []byte
	ObjectKey string
	SHA       string
	SizeBytes int64
	Error     string
	CreatedAt time.Time
}

type Run struct {
	ID, OwnerID, ProjectID, IdempotencyKey string
	InputMessageID, AssistantMessageID     string
	InputMessage                           string
	Status, AgentRunID, WorkerID           string
	TraceParent, TraceState                string
	LastSequence                           int64
	ErrorCode, ErrorMessage                string
	CreatedAt, UpdatedAt                   time.Time
	StartedAt, HeartbeatAt, CompletedAt    *time.Time
	CancelRequestedAt                      *time.Time
}

type CreateRunInput struct {
	ID, OwnerID, ProjectID, IdempotencyKey string
	InputMessageID, AssistantMessageID     string
	Message                                string
	TraceParent, TraceState                string
	Now                                    time.Time
}

type ProjectRunState struct {
	RuntimeStatus string
	ActiveRunID   *string
	LastRunID     *string
}

type MessageListReq struct {
	Cursor string `form:"cursor"`
	Limit  int    `form:"limit"`
}

type MessageItem struct {
	ID        string  `json:"id"`
	RunID     *string `json:"run_id,omitempty"`
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

type MessageListResp struct {
	Items      []MessageItem `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

type Message struct {
	ID, ProjectID, OwnerID, Role, Content, Status string
	RunID                                         *string
	CreatedAt, UpdatedAt                          time.Time
}

type ProjectRuntime struct {
	ProjectID, OwnerID, GatewaySessionID, AgentConversationID, Status string
	CreatedAt, LastActiveAt, ExpiresAt, UpdatedAt                     time.Time
}

type FileTreeReq struct {
	Path string `form:"path"`
}

type FileNode struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size,omitempty"`
	ModTime string `json:"mod_time,omitempty"`
}

type FileTreeResp struct {
	Root  string     `json:"root"`
	Nodes []FileNode `json:"nodes"`
}

type FileContentReq struct {
	Path string `form:"path" binding:"required"`
}

type FileContentResp struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

type FileContentUpdateReq struct {
	Content string  `json:"content"`
	SHA     *string `json:"sha" binding:"required"`
}

type FileContentUpdateResp struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

type PreviewStartReq struct {
	Port int `json:"port" binding:"required,min=1,max=65535"`
}

type PreviewResp struct {
	PreviewID       string `json:"preview_id"`
	Status          string `json:"status"`
	PreviewURL      string `json:"preview_url"`
	Port            int    `json:"port"`
	ExpiresAt       string `json:"expires_at"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
}

type ProjectPreview struct {
	ID, ProjectID, OwnerID, Status, PreviewURL, PreviewToken string
	Port                                                     int
	CreatedAt, LastActiveAt, ExpiresAt, UpdatedAt            time.Time
}

type PublicationCreateReq struct {
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile"`
}

type PublicationResp struct {
	ID                string  `json:"id"`
	ProjectID         string  `json:"project_id"`
	Status            string  `json:"status"`
	Context           string  `json:"context"`
	Dockerfile        string  `json:"dockerfile"`
	ImageRef          string  `json:"image_ref,omitempty"`
	Digest            string  `json:"digest,omitempty"`
	Logs              string  `json:"logs,omitempty"`
	ErrorCode         string  `json:"error_code,omitempty"`
	ErrorMessage      string  `json:"error_message,omitempty"`
	CancelRequestedAt *string `json:"cancel_requested_at,omitempty"`
	CreatedAt         string  `json:"created_at"`
	StartedAt         *string `json:"started_at,omitempty"`
	CompletedAt       *string `json:"completed_at,omitempty"`
}

type PublicationListResp struct {
	Items []PublicationResp `json:"items"`
}

type PublicationCancelResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type Publication struct {
	ID, OwnerID, ProjectID, IdempotencyKey string
	Context, Dockerfile, Status, WorkerID  string
	ImageRef, Digest, Logs                 string
	ErrorCode, ErrorMessage                string
	TraceParent, TraceState                string
	CreatedAt, UpdatedAt                   time.Time
	StartedAt, HeartbeatAt, CompletedAt    *time.Time
	CancelRequestedAt                      *time.Time
}

type CreatePublicationInput struct {
	ID, OwnerID, ProjectID, IdempotencyKey string
	Context, Dockerfile                    string
	TraceParent, TraceState                string
	Now                                    time.Time
}

type FinishPublicationInput struct {
	ID, WorkerID, Status    string
	ImageRef, Digest, Logs  string
	ErrorCode, ErrorMessage string
	Now                     time.Time
}

type GatewayPublication struct {
	ImageRef string `json:"image_ref"`
	Digest   string `json:"digest"`
	Logs     string `json:"logs"`
}

type AgentEvent struct {
	Type           string          `json:"type"`
	RunID          string          `json:"run_id"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Sequence       int64           `json:"sequence,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

type StoredRunEvent struct {
	ID, Type string
	Data     json.RawMessage
}

type RunSequence struct {
	RunID    string
	Sequence int64
}
