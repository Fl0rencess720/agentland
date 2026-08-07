package models

import (
	"fmt"
	"time"
)

type GatewayResponseError struct {
	StatusCode int
	Code       string
	Message    string
	SHA        string
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
	SessionID, PreviewToken, PreviewURL string
	Port                                int
	ExpiresAt                           time.Time
}

type GatewayFileTree struct {
	Root  string
	Nodes []FileNode
}

type GatewayFile struct {
	Path, Content, SHA string
	Size               int64
}

type GatewayFileWrite struct {
	Path, SHA string
	Size      int64
}
