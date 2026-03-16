package models

import "time"

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
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Status       string           `json:"status"`
	OwnerID      string           `json:"owner_id"`
	LastOpenedAt string           `json:"last_opened_at"`
	Metadata     *ProjectMetadata `json:"metadata,omitempty"`
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
	OwnerID   string
	Keyword   string
	Status    string
	SortBy    string
	SortOrder string
	Page      int
	PageSize  int
	View      string
}

type CreateProjectInput struct {
	ID       string
	OwnerID  string
	Name     string
	Template string
	Status   string
	Now      time.Time
}

type UpdateProjectInput struct {
	ProjectID string
	OwnerID   string
	Name      string
	Metadata  ProjectMetadata
	Now       time.Time
}

type AttachmentRef struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
}

type GenerationCreateReq struct {
	Prompt      string          `json:"prompt" binding:"required"`
	Attachments []AttachmentRef `json:"attachments"`
	Deep        bool            `json:"deep"`
}

type GenerationCreateResp struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

type ChatMessagesReq struct {
	Cursor string `form:"cursor"`
}

type ChatMessageItem struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type ChatMessagesResp struct {
	Items      []ChatMessageItem `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

type ChatMessageCreateReq struct {
	Content     string          `json:"content" binding:"required"`
	Attachments []AttachmentRef `json:"attachments"`
	Deep        bool            `json:"deep"`
}

type ChatMessageStreamDeltaResp struct {
	Text string `json:"text"`
}

type FileChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type ChatMessageStreamDoneResp struct {
	MessageID string       `json:"message_id"`
	Changes   []FileChange `json:"changes"`
}

type ProjectChatSession struct {
	ProjectID          string
	OwnerID            string
	GatewaySessionID   string
	AgentChatSessionID string
	WorkspacePath      string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastMessageAt      time.Time
}

type ProjectChatMessage struct {
	ID        string
	ProjectID string
	OwnerID   string
	Role      string
	Content   string
	CreatedAt time.Time
}

type UpsertProjectChatSessionInput struct {
	ProjectID          string
	OwnerID            string
	GatewaySessionID   string
	AgentChatSessionID string
	WorkspacePath      string
	Now                time.Time
}

type CreateProjectChatMessageInput struct {
	ID        string
	ProjectID string
	OwnerID   string
	Role      string
	Content   string
	Now       time.Time
}

type FileTreeReq struct {
	Path  string `form:"path" binding:"required"`
	Depth int    `form:"depth"`
}

type FileNode struct {
	Path     string     `json:"path"`
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Size     int64      `json:"size,omitempty"`
	Children []FileNode `json:"children,omitempty"`
}

type FileTreeResp struct {
	Root  string     `json:"root"`
	Nodes []FileNode `json:"nodes"`
}

type FileContentReq struct {
	Path string `form:"path" binding:"required"`
}

type FileContentResp struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Content  string `json:"content"`
	SHA      string `json:"sha"`
}

type PreviewStartReq struct {
	Device string `json:"device"`
	Port   int    `json:"port"`
}

type PreviewStartResp struct {
	PreviewID  string `json:"preview_id"`
	Status     string `json:"status"`
	PreviewURL string `json:"preview_url"`
}

type PreviewStatusResp struct {
	PreviewID       string `json:"preview_id"`
	Status          string `json:"status"`
	PreviewURL      string `json:"preview_url"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
}

type PublishReq struct {
	Channel     string `json:"channel"`
	VersionNote string `json:"version_note"`
}

type PublishResp struct {
	ReleaseID string `json:"release_id"`
	PublicURL string `json:"public_url"`
	Version   string `json:"version"`
}

type DeploymentCreateReq struct {
	Environment  string            `json:"environment"`
	BuildCommand string            `json:"build_command"`
	OutputDir    string            `json:"output_dir"`
	Env          map[string]string `json:"env"`
}

type DeploymentCreateResp struct {
	DeploymentID string `json:"deployment_id"`
	Status       string `json:"status"`
}

type ShareCreateReq struct {
	Scope     string `json:"scope"`
	ExpiresAt string `json:"expires_at"`
	Password  string `json:"password"`
}

type ShareCreateResp struct {
	ShareID  string `json:"share_id"`
	ShareURL string `json:"share_url"`
}

type ShareDeleteResp struct {
	Success bool `json:"success"`
}
