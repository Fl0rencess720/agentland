package handlers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFSHandler_GetTree_HidesDotFilesByDefault(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "visible.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".hidden.txt"), []byte("secret"), 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/tree?path=.&depth=5", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp treeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	paths := make([]string, 0, len(resp.Nodes))
	for _, n := range resp.Nodes {
		paths = append(paths, n.Path)
	}
	require.Contains(t, paths, "visible.txt")
	require.NotContains(t, paths, ".hidden.txt")
}

func TestFSHandler_GetTree_IncludeHidden(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".hidden.txt"), []byte("secret"), 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/tree?path=.&depth=5&includeHidden=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp treeResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Nodes, 1)
	require.Equal(t, ".hidden.txt", resp.Nodes[0].Path)
}

func TestFSHandler_GetTree_RejectTraversal(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/tree?path=../../etc&depth=5", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestFSHandler_GetFile_UTF8(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.ts"), []byte("console.log('hello')\n"), 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/file?path=main.ts&encoding=utf8", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp fileResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "main.ts", resp.Path)
	require.Equal(t, "utf8", resp.Encoding)
	require.Contains(t, resp.Content, "console.log")
}

func TestFSHandler_GetFile_Base64(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	bin := []byte{0xff, 0xfe, 0xfd}
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin.dat"), bin, 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/file?path=bin.dat&encoding=base64", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp fileResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, base64.StdEncoding.EncodeToString(bin), resp.Content)
}

func TestFSHandler_GetFile_RejectInvalidUTF8(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0xff, 0xfe}, 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/file?path=bin.dat&encoding=utf8", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFSHandler_GetFile_TooLarge(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), []byte("123456"), 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 5)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/file?path=big.txt", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
