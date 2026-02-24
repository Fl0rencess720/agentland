package handlers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
)

const (
	// 文件时间字段统一使用的 RFC3339 格式模板
	timeLayoutRFC3339 = "2006-01-02T15:04:05Z07:00"
	// 文件读写接口的默认文本编码
	defaultFileEncoding = "utf8"
)

var errPathEscapesWorkspaceRoot = errors.New("path escapes workspace root")

// FSHandler 封装文件系统相关接口所需的运行参数
type FSHandler struct {
	workspaceRoot string
	maxFileBytes  int64
}

// InitFSApi 注册 fs 相关 HTTP 路由并初始化处理器
func InitFSApi(group *gin.RouterGroup, workspaceRoot string, maxFileBytes int64) {
	h := &FSHandler{
		workspaceRoot: workspaceRoot,
		maxFileBytes:  maxFileBytes,
	}
	group.GET("/fs/tree", h.GetFSTree)
	group.GET("/fs/file", h.GetFSFile)
	group.POST("/fs/file", h.WriteFSFile)
	group.POST("/fs/upload", h.UploadFSFile)
	group.GET("/fs/download", h.DownloadFSFile)
}

// GetFSTree 根据路径返回目录树，支持深度控制和是否包含隐藏文件
func (h *FSHandler) GetFSTree(c *gin.Context) {
	rootPath := strings.TrimSpace(c.DefaultQuery("path", "."))
	depth, err := parseDepth(c.DefaultQuery("depth", "5"))
	if err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}
	includeHidden, err := parseIncludeHidden(c.DefaultQuery("includeHidden", "false"))
	if err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}
	targetPath, cleanedRoot, err := resolveWorkspacePath(h.workspaceRoot, rootPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			response.ErrorResponse(c, response.FormError)
			return
		}
		response.ErrorResponse(c, response.ServerError)
		return
	}
	if !info.IsDir() {
		response.ErrorResponse(c, response.FormError)
		return
	}

	nodes := make([]models.FSTreeNode, 0)
	walkErr := filepath.WalkDir(targetPath, func(curr string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if curr == targetPath {
			return nil
		}

		rel, err := filepath.Rel(targetPath, curr)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if !includeHidden && containsHiddenSegment(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if depth > 0 && pathDepth(rel) > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		node := models.FSTreeNode{
			Path: rel,
			Name: d.Name(),
		}
		if d.IsDir() {
			node.Type = "dir"
			nodes = append(nodes, node)
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		node.Type = "file"
		node.Size = info.Size()
		node.ModTime = info.ModTime().UTC().Format(timeLayoutRFC3339)
		nodes = append(nodes, node)
		return nil
	})
	if walkErr != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Path < nodes[j].Path
	})

	response.SuccessResponse(c, models.GetFSTreeResp{
		Root:  filepath.ToSlash(cleanedRoot),
		Nodes: nodes,
	})
}

// GetFSFile 读取指定文件内容，支持 utf8/base64 编码返回
func (h *FSHandler) GetFSFile(c *gin.Context) {
	filePath := strings.TrimSpace(c.Query("path"))
	if filePath == "" {
		response.ErrorResponse(c, response.FormError)
		return
	}

	encoding, err := parseEncoding(c.DefaultQuery("encoding", "utf8"))
	if err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}
	targetPath, cleanedPath, err := resolveWorkspacePath(h.workspaceRoot, filePath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	info, err := os.Lstat(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			response.ErrorResponse(c, response.FormError)
			return
		}
		response.ErrorResponse(c, response.ServerError)
		return
	}
	if info.IsDir() {
		response.ErrorResponse(c, response.FormError)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		response.ErrorResponse(c, response.FormError)
		return
	}
	if h.maxFileBytes > 0 && info.Size() > h.maxFileBytes {
		response.ErrorResponse(c, response.FormError)
		return
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	content := ""
	if encoding == defaultFileEncoding {
		if !utf8.Valid(data) {
			response.ErrorResponse(c, response.FormError)
			return
		}
		content = string(data)
	} else {
		content = base64.StdEncoding.EncodeToString(data)
	}

	response.SuccessResponse(c, models.GetFSFileResp{
		Path:     filepath.ToSlash(cleanedPath),
		Size:     int64(len(data)),
		Encoding: encoding,
		Content:  content,
	})
}

// WriteFSFile 将请求内容按指定编码写入目标文件
func (h *FSHandler) WriteFSFile(c *gin.Context) {
	var req models.WriteFSFileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}

	path := strings.TrimSpace(req.Path)
	if path == "" {
		response.ErrorResponse(c, response.FormError)
		return
	}

	encoding, err := parseEncoding(req.Encoding)
	if err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}
	targetPath, cleanedPath, err := resolveWorkspacePath(h.workspaceRoot, path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	data, err := decodeContent(req.Content, encoding)
	if err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}

	if err := ensureParentDir(targetPath); err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	response.SuccessResponse(c, models.WriteFSFileResp{
		Path:     filepath.ToSlash(cleanedPath),
		Size:     int64(len(data)),
		Encoding: encoding,
	})
}

// UploadFSFile 接收调用方上传的文件流并写入沙箱目标路径
func (h *FSHandler) UploadFSFile(c *gin.Context) {
	targetPath := strings.TrimSpace(c.PostForm("target_file_path"))
	if targetPath == "" {
		targetPath = strings.TrimSpace(c.Query("target_file_path"))
	}
	if targetPath == "" {
		response.ErrorResponse(c, response.FormError)
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}
	defer file.Close()

	resolvedTargetPath, cleanedTargetPath, err := resolveWorkspacePath(h.workspaceRoot, targetPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if err := ensureParentDir(resolvedTargetPath); err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	target, err := os.OpenFile(resolvedTargetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}
	defer target.Close()

	size, err := io.Copy(target, file)
	if err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	response.SuccessResponse(c, models.UploadFSFileResp{
		SourcePath: header.Filename,
		TargetPath: filepath.ToSlash(cleanedTargetPath),
		Size:       size,
	})
}

// DownloadFSFile 将沙箱文件以二进制流返回给调用方
func (h *FSHandler) DownloadFSFile(c *gin.Context) {
	sourcePath := strings.TrimSpace(c.Query("path"))
	if sourcePath == "" {
		response.ErrorResponse(c, response.FormError)
		return
	}

	resolvedSourcePath, cleanedSourcePath, err := resolveWorkspacePath(h.workspaceRoot, sourcePath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	info, err := os.Lstat(resolvedSourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			response.ErrorResponse(c, response.FormError)
			return
		}
		response.ErrorResponse(c, response.ServerError)
		return
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		response.ErrorResponse(c, response.FormError)
		return
	}
	if h.maxFileBytes > 0 && info.Size() > h.maxFileBytes {
		response.ErrorResponse(c, response.FormError)
		return
	}

	fileName := filepath.Base(cleanedSourcePath)
	if fileName == "." || fileName == string(filepath.Separator) || fileName == "" {
		fileName = "download.bin"
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("X-Agentland-File-Path", filepath.ToSlash(cleanedSourcePath))
	c.File(resolvedSourcePath)
}

// parseDepth 解析并校验目录遍历深度参数
func parseDepth(v string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, errors.New("depth must be an integer")
	}
	if parsed < 1 || parsed > 20 {
		return 0, errors.New("depth must be between 1 and 20")
	}
	return parsed, nil
}

// parseIncludeHidden 解析是否包含隐藏文件参数
func parseIncludeHidden(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1":
		return true, nil
	case "false", "0", "":
		return false, nil
	default:
		return false, errors.New("includeHidden must be true or false")
	}
}

// containsHiddenSegment 判断相对路径是否包含隐藏路径段
func containsHiddenSegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") && seg != "." && seg != ".." {
			return true
		}
	}
	return false
}

// pathDepth 计算相对路径层级深度
func pathDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

// parseEncoding 解析并规范化编码参数
func parseEncoding(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "utf8", "utf-8":
		return defaultFileEncoding, nil
	case "base64":
		return "base64", nil
	default:
		return "", errors.New("encoding must be utf8, utf-8 or base64")
	}
}

// decodeContent 按指定编码将请求中的内容解码为字节流
func decodeContent(content, encoding string) ([]byte, error) {
	switch encoding {
	case defaultFileEncoding:
		return []byte(content), nil
	case "base64":
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, errors.New("content is not valid base64")
		}
		return data, nil
	default:
		return nil, errors.New("unsupported encoding")
	}
}

// resolveWorkspacePath 将请求路径解析为实际路径，并返回清洗后的路径字符串
func resolveWorkspacePath(workspaceRoot, requested string) (string, string, error) {
	root := filepath.Clean(workspaceRoot)
	path := strings.TrimSpace(requested)
	if path == "" {
		path = "."
	}
	cleanedPath := filepath.Clean(path)
	if filepath.IsAbs(cleanedPath) {
		return cleanedPath, cleanedPath, nil
	}

	target := filepath.Clean(filepath.Join(root, cleanedPath))
	relToRoot, err := filepath.Rel(root, target)
	if err != nil {
		return "", "", err
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", "", errPathEscapesWorkspaceRoot
	}
	return target, cleanedPath, nil
}

// ensureParentDir 确保目标文件的父目录存在，不存在则自动创建
func ensureParentDir(path string) error {
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return nil
	}
	return os.MkdirAll(parent, 0o755)
}
