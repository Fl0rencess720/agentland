package agentd

import (
	"crypto/sha256"
	"encoding/hex"
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
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	defaultWorkspaceTreeDepth = 5
	maxWorkspaceTreeDepth     = 20
	maxWorkspaceTreeNodes     = 2048
	maxWorkspaceFileBytes     = 1 << 20
	maxWorkspaceWriteBody     = maxWorkspaceFileBytes*6 + 1024
)

var errWorkspaceTreeLimit = errors.New("workspace tree limit reached")

var workspaceTreeIgnoredDirs = map[string]struct{}{
	".agentland":   {},
	".git":         {},
	".next":        {},
	".venv":        {},
	"__pycache__":  {},
	"node_modules": {},
}

type workspaceHandler struct {
	tools *localTools
}

type workspaceTreeNode struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size,omitempty"`
	ModTime string `json:"mod_time,omitempty"`
}

type workspaceFileResponse struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Content string `json:"content"`
	SHA     string `json:"sha"`
}

type workspaceWriteResponse struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

type workspaceWriteRequest struct {
	Content *string `json:"content"`
	SHA     *string `json:"sha"`
}

func (h *workspaceHandler) tree(c *gin.Context) {
	depth, err := workspaceTreeDepth(c.Query("depth"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	requested := strings.TrimSpace(c.Query("path"))
	if requested == "" {
		requested = "."
	}
	name, err := h.tools.workspaceName(requested)
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	if err := ensureInternalAccess(name, false); err != nil {
		h.workspaceError(c, err)
		return
	}
	root, err := os.OpenRoot(h.tools.root)
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	defer root.Close()
	treeRoot, err := h.tools.openRootDir(root, name, false)
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	defer treeRoot.Close()
	info, err := treeRoot.Stat(".")
	if err != nil || !info.IsDir() {
		if err != nil {
			h.workspaceError(c, err)
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is not a directory"})
		return
	}
	nodes := make([]workspaceTreeNode, 0)
	err = fs.WalkDir(treeRoot.FS(), ".", func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return workspaceRootError(walkErr)
		}
		if current == "." {
			return nil
		}
		if entry.IsDir() {
			if _, ignored := workspaceTreeIgnoredDirs[entry.Name()]; ignored {
				return filepath.SkipDir
			}
		}
		relative, err := filepath.Rel(".", current)
		if err != nil {
			return err
		}
		if treePathDepth(relative) > depth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if len(nodes) >= maxWorkspaceTreeNodes {
			return errWorkspaceTreeLimit
		}
		node := workspaceTreeNode{
			Path: filepath.ToSlash(relative),
			Name: entry.Name(),
			Type: "dir",
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			node.Type = "file"
			node.Size = info.Size()
			node.ModTime = info.ModTime().UTC().Format(time.RFC3339)
		}
		nodes = append(nodes, node)
		return nil
	})
	if errors.Is(err, errWorkspaceTreeLimit) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("workspace tree exceeds %d nodes; narrow path or depth", maxWorkspaceTreeNodes),
		})
		return
	}
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })
	c.JSON(http.StatusOK, gin.H{
		"root":  filepath.ToSlash(filepath.Join(h.tools.root, name)),
		"nodes": nodes,
	})
}

func (h *workspaceHandler) readFile(c *gin.Context) {
	requested := strings.TrimSpace(c.Query("path"))
	if requested == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	name, err := h.tools.workspaceName(requested)
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	if err := ensureInternalAccess(name, false); err != nil {
		h.workspaceError(c, err)
		return
	}
	root, err := os.OpenRoot(h.tools.root)
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	defer root.Close()
	file, err := h.tools.openRootFile(root, name, false)
	if err != nil {
		h.workspaceError(c, workspaceRootError(err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	if !info.Mode().IsRegular() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is not a regular file"})
		return
	}
	if info.Size() > maxWorkspaceFileBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("file exceeds %d bytes", maxWorkspaceFileBytes),
		})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxWorkspaceFileBytes+1))
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	if len(data) > maxWorkspaceFileBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("file exceeds %d bytes", maxWorkspaceFileBytes),
		})
		return
	}
	if !utf8.Valid(data) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is not valid UTF-8 text"})
		return
	}
	c.JSON(http.StatusOK, workspaceFileResponse{
		Path:    filepath.ToSlash(name),
		Size:    int64(len(data)),
		Content: string(data),
		SHA:     fileSHA(data),
	})
}

func (h *workspaceHandler) writeFile(c *gin.Context) {
	requested := strings.TrimSpace(c.Query("path"))
	if requested == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWorkspaceWriteBody)
	var request workspaceWriteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if request.Content == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content is required"})
		return
	}
	if len(*request.Content) > maxWorkspaceFileBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("content exceeds %d bytes", maxWorkspaceFileBytes),
		})
		return
	}

	h.tools.writeMu.Lock()
	defer h.tools.writeMu.Unlock()
	name, err := h.tools.workspaceName(requested)
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	if err := ensureInternalAccess(name, false); err != nil {
		h.workspaceError(c, err)
		return
	}
	root, err := os.OpenRoot(h.tools.root)
	if err != nil {
		h.workspaceError(c, err)
		return
	}
	defer root.Close()
	if info, statErr := root.Lstat(name); statErr == nil && info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is not a regular file"})
		return
	}
	if request.SHA != nil {
		currentFile, openErr := h.tools.openRootFile(root, name, false)
		currentSHA := ""
		if openErr == nil {
			currentSHA, err = fileHandleSHA(currentFile)
			currentFile.Close()
		} else {
			err = openErr
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			h.workspaceError(c, workspaceRootError(err))
			return
		}
		if strings.TrimSpace(*request.SHA) != currentSHA {
			c.JSON(http.StatusConflict, gin.H{
				"code":  "FILE_CONFLICT",
				"error": "file has changed",
				"sha":   currentSHA,
			})
			return
		}
	}
	if _, err := h.tools.writeFileUnlocked(writeFileInput{Path: name, Content: *request.Content}); err != nil {
		h.workspaceError(c, err)
		return
	}
	data := []byte(*request.Content)
	c.JSON(http.StatusOK, workspaceWriteResponse{
		Path: filepath.ToSlash(name),
		Size: int64(len(data)),
		SHA:  fileSHA(data),
	})
}

func (h *workspaceHandler) workspaceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		c.JSON(http.StatusNotFound, gin.H{"error": "path not found"})
	case errors.Is(err, errWorkspaceInternal):
		c.JSON(http.StatusForbidden, gin.H{"error": "path is internal to agentd"})
	case strings.Contains(err.Error(), "escapes workspace"):
		c.JSON(http.StatusForbidden, gin.H{"error": "path escapes workspace"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace operation failed"})
	}
}

func workspaceTreeDepth(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultWorkspaceTreeDepth, nil
	}
	depth, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || depth < 1 || depth > maxWorkspaceTreeDepth {
		return 0, errors.New("depth must be between 1 and 20")
	}
	return depth, nil
}

func treePathDepth(path string) int {
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == "" {
		return 0
	}
	return strings.Count(filepath.ToSlash(cleaned), "/") + 1
}

func currentFileSHAAt(root *os.Root, name string) (string, error) {
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return fileHandleSHA(file)
}

func fileHandleSHA(file *os.File) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func fileSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
