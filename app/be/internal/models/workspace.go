package models

import (
	"fmt"
	"time"
)

type GatewayResponseError struct {
	StatusCode int
	Message    string
}

func (e *GatewayResponseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("gateway request failed with status %d", e.StatusCode)
	}
	return e.Message
}

type GatewayPreviewInfo struct {
	SessionID    string
	Port         int
	PreviewToken string
	PreviewURL   string
	ExpiresAt    time.Time
}

type GatewayFSTreeNode struct {
	Path    string
	Name    string
	Type    string
	Size    int64
	ModTime string
}

type GatewayFSTreeResp struct {
	Root  string
	Nodes []GatewayFSTreeNode
}

type GatewayFSFileResp struct {
	Path     string
	Size     int64
	Encoding string
	Content  string
}

type GatewayExecContextInfo struct {
	ContextID string
	Language  string
	CWD       string
	State     string
	CreatedAt string
}

type GatewayExecutionResult struct {
	ContextID      string
	ExecutionID    string
	ExecutionCount int64
	ExitCode       int32
	Stdout         string
	Stderr         string
	DurationMs     int64
}

type WorkspaceArchive struct {
	FileName    string
	ContentType string
	Content     []byte
}
