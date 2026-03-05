package jupyter

import "encoding/json"

type KernelSpecs struct {
	Kernelspecs map[string]*KernelSpecInfo `json:"kernelspecs"`
}

type KernelSpecInfo struct {
	Spec KernelSpec `json:"spec"`
}

type KernelSpec struct {
	Language string `json:"language"`
}

type Session struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Kernel Kernel `json:"kernel"`
}

type Kernel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type messageHeader struct {
	MessageID   string `json:"msg_id"`
	Username    string `json:"username"`
	Session     string `json:"session"`
	Date        string `json:"date"`
	MessageType string `json:"msg_type"`
	Version     string `json:"version"`
}

type wireMessage struct {
	Header       messageHeader     `json:"header"`
	ParentHeader messageHeader     `json:"parent_header"`
	Metadata     map[string]any    `json:"metadata"`
	Content      json.RawMessage   `json:"content"`
	Channel      string            `json:"channel"`
	Buffers      []json.RawMessage `json:"buffers,omitempty"`
}
