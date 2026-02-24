package models

// GetFSTreeReq 对应 GET /fs/tree 的查询参数
type GetFSTreeReq struct {
	Path          string `json:"path" jsonschema:"Directory path to traverse, relative or absolute"`
	Depth         int    `json:"depth" jsonschema:"Traversal depth, valid range is 1-20"`
	IncludeHidden bool   `json:"includeHidden" jsonschema:"Whether to include hidden files and directories"`
}

// GetFSTreeResp 获取文件树接口返回的数据结构
type GetFSTreeResp struct {
	Root  string       `json:"root" jsonschema:"Normalized root path of the tree"`
	Nodes []FSTreeNode `json:"nodes" jsonschema:"File and directory nodes under the root"`
}

// FSTreeNode 文件树中的单个节点
type FSTreeNode struct {
	Path    string `json:"path" jsonschema:"Relative path from root"`
	Name    string `json:"name" jsonschema:"Base name of the node"`
	Type    string `json:"type" jsonschema:"Node type, one of: dir, file"`
	Size    int64  `json:"size,omitempty" jsonschema:"File size in bytes, only for files"`
	ModTime string `json:"modTime,omitempty" jsonschema:"Last modified time in RFC3339 format, only for files"`
}

// GetFSFileReq 对应 GET /fs/file 的查询参数
type GetFSFileReq struct {
	Path     string `json:"path" jsonschema:"File path to read, relative or absolute"`
	Encoding string `json:"encoding,omitempty" jsonschema:"Content encoding, supported values: utf8, utf-8, base64"`
}

// GetFSFileResp 读取文件接口响应体
type GetFSFileResp struct {
	Path     string `json:"path" jsonschema:"Normalized file path"`
	Size     int64  `json:"size" jsonschema:"File size in bytes"`
	Encoding string `json:"encoding" jsonschema:"Returned content encoding"`
	Content  string `json:"content" jsonschema:"File content encoded by the encoding field"`
}

// WriteFSFileReq 写入文件接口请求体
type WriteFSFileReq struct {
	Path     string `json:"path" jsonschema:"Destination file path, relative or absolute"`
	Content  string `json:"content" jsonschema:"File content to write"`
	Encoding string `json:"encoding,omitempty" jsonschema:"Input content encoding, supported values: utf8, utf-8, base64"`
}

// WriteFSFileResp 写入文件接口响应体
type WriteFSFileResp struct {
	Path     string `json:"path" jsonschema:"Normalized written file path"`
	Size     int64  `json:"size" jsonschema:"Written content size in bytes"`
	Encoding string `json:"encoding" jsonschema:"Resolved encoding used to decode input content"`
}

// UploadFSFileReq 对应 POST /fs/upload 的请求体（MCP 友好形式）
type UploadFSFileReq struct {
	TargetFilePath string `json:"target_file_path" jsonschema:"Destination file path in sandbox, relative or absolute"`
	FileName       string `json:"file_name,omitempty" jsonschema:"Original file name"`
	ContentBase64  string `json:"content_base64" jsonschema:"File content in base64"`
}

// UploadFSFileResp 上传文件接口响应体
type UploadFSFileResp struct {
	SourcePath string `json:"source_path" jsonschema:"Source file name"`
	TargetPath string `json:"target_path" jsonschema:"Normalized destination file path"`
	Size       int64  `json:"size" jsonschema:"Uploaded file size in bytes"`
}

// DownloadFSFileReq 对应 GET /fs/download 的查询参数
type DownloadFSFileReq struct {
	Path string `json:"path" jsonschema:"Source file path to download, relative or absolute"`
}

// DownloadFSFileResp 下载文件接口响应体（MCP 友好形式）
type DownloadFSFileResp struct {
	SourcePath    string `json:"source_path" jsonschema:"Normalized source file path"`
	FileName      string `json:"file_name" jsonschema:"Downloaded file name"`
	Size          int64  `json:"size" jsonschema:"Downloaded file size in bytes"`
	ContentBase64 string `json:"content_base64" jsonschema:"Downloaded file content in base64"`
}
