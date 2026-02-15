package handlers

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type FSHandler struct {
	workspaceRoot string
	maxFileBytes  int64
}

type treeResponse struct {
	Root  string     `json:"root"`
	Nodes []treeNode `json:"nodes"`
}

type treeNode struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size,omitempty"`
	ModTime string `json:"modTime,omitempty"`
}

type fileResponse struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

func InitFSApi(group *gin.RouterGroup, workspaceRoot string, maxFileBytes int64) {
	h := &FSHandler{
		workspaceRoot: workspaceRoot,
		maxFileBytes:  maxFileBytes,
	}
	group.GET("/fs/tree", h.GetTree)
	group.GET("/fs/file", h.GetFile)
}

func (h *FSHandler) GetTree(c *gin.Context) {
	rootPath := strings.TrimSpace(c.DefaultQuery("path", "."))
	depth, err := parseDepth(c.DefaultQuery("depth", "5"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	includeHidden, err := parseIncludeHidden(c.DefaultQuery("includeHidden", "false"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
			c.JSON(http.StatusNotFound, gin.H{"error": "path not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stat path failed"})
		return
	}
	if !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path must be a directory"})
		return
	}

	nodes := make([]treeNode, 0)
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

		node := treeNode{
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "walk directory failed"})
		return
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Path < nodes[j].Path
	})

	c.JSON(http.StatusOK, treeResponse{
		Root:  filepath.ToSlash(cleanedRoot),
		Nodes: nodes,
	})
}

func (h *FSHandler) GetFile(c *gin.Context) {
	filePath := strings.TrimSpace(c.Query("path"))
	if filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}

	encoding := strings.TrimSpace(strings.ToLower(c.DefaultQuery("encoding", "utf8")))
	if encoding != "utf8" && encoding != "base64" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "encoding must be utf8 or base64"})
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
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stat file failed"})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path must be a file"})
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "symlink is not allowed"})
		return
	}
	if h.maxFileBytes > 0 && info.Size() > h.maxFileBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds size limit"})
		return
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read file failed"})
		return
	}

	content := ""
	if encoding == "utf8" {
		if !utf8.Valid(data) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file is not valid utf8, use encoding=base64"})
			return
		}
		content = string(data)
	} else {
		content = base64.StdEncoding.EncodeToString(data)
	}

	c.JSON(http.StatusOK, fileResponse{
		Path:     filepath.ToSlash(cleanedPath),
		Size:     int64(len(data)),
		Encoding: encoding,
		Content:  content,
	})
}

const timeLayoutRFC3339 = "2006-01-02T15:04:05Z07:00"

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

func containsHiddenSegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") && seg != "." && seg != ".." {
			return true
		}
	}
	return false
}

func pathDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/") + 1
}

func resolveWorkspacePath(workspaceRoot, requested string) (string, string, error) {
	root := filepath.Clean(workspaceRoot)
	rel := strings.TrimSpace(requested)
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) {
		return "", "", errors.New("absolute path is not allowed")
	}

	cleanedRel := filepath.Clean(rel)
	target := filepath.Clean(filepath.Join(root, cleanedRel))
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", "", errors.New("path escapes workspace root")
	}
	return target, cleanedRel, nil
}
