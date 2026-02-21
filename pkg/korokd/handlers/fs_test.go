package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fsSuccessResponse struct {
	Msg  string          `json:"msg"`
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func decodeFSSuccessData(t *testing.T, body []byte, out interface{}) {
	t.Helper()

	var resp fsSuccessResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, "success", resp.Msg)
	require.Equal(t, http.StatusOK, resp.Code)
	require.NoError(t, json.Unmarshal(resp.Data, out))
}

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

	var resp GetFSTreeResp
	decodeFSSuccessData(t, w.Body.Bytes(), &resp)

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

	var resp GetFSTreeResp
	decodeFSSuccessData(t, w.Body.Bytes(), &resp)
	require.Len(t, resp.Nodes, 1)
	require.Equal(t, ".hidden.txt", resp.Nodes[0].Path)
}

func TestFSHandler_GetTree_AllowsAbsolutePath(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	absRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(absRoot, "outside.txt"), []byte("outside"), 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/tree?path="+url.QueryEscape(absRoot)+"&depth=5", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp GetFSTreeResp
	decodeFSSuccessData(t, w.Body.Bytes(), &resp)
	require.Equal(t, filepath.ToSlash(filepath.Clean(absRoot)), resp.Root)
	require.Len(t, resp.Nodes, 1)
	require.Equal(t, "outside.txt", resp.Nodes[0].Path)
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

	var resp GetFSFileResp
	decodeFSSuccessData(t, w.Body.Bytes(), &resp)
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

	var resp GetFSFileResp
	decodeFSSuccessData(t, w.Body.Bytes(), &resp)
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
	require.Contains(t, w.Body.String(), `"msg":"Form Error"`)
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
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), `"msg":"Form Error"`)
}

func TestFSHandler_WriteFile_UTF8(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "data.txt")

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	reqBody := WriteFSFileReq{
		Path:     targetPath,
		Content:  "这是测试数据\n第二行数据",
		Encoding: "utf-8",
	}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/fs/file", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp WriteFSFileResp
	decodeFSSuccessData(t, w.Body.Bytes(), &resp)
	require.Equal(t, filepath.ToSlash(filepath.Clean(targetPath)), resp.Path)

	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, reqBody.Content, string(data))
}

func TestFSHandler_UploadFile(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "dataset.csv")

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "dataset.csv")
	require.NoError(t, err)
	_, err = part.Write([]byte("name,value\nalice,1\n"))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("target_file_path", targetPath))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/fs/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp TransferFSFileResp
	decodeFSSuccessData(t, w.Body.Bytes(), &resp)
	require.Equal(t, "dataset.csv", resp.SourcePath)
	require.Equal(t, filepath.ToSlash(filepath.Clean(targetPath)), resp.TargetPath)

	data, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, "name,value\nalice,1\n", string(data))
}

func TestFSHandler_UploadFile_RejectJSONBody(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	reqBody := UploadFSFileReq{
		LocalFilePath:  "/tmp/a.csv",
		TargetFilePath: "/workspace/a.csv",
	}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/fs/upload", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), `"msg":"Form Error"`)
}

func TestFSHandler_DownloadFile(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	root := t.TempDir()
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "result.csv")
	require.NoError(t, os.WriteFile(sourcePath, []byte("id,score\n1,100\n"), 0o644))

	router := gin.New()
	group := router.Group("/api")
	InitFSApi(group, root, 1024)

	req := httptest.NewRequest(http.MethodGet, "/api/fs/download?path="+url.QueryEscape(sourcePath), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "id,score\n1,100\n", w.Body.String())
	require.Contains(t, w.Header().Get("Content-Disposition"), "result.csv")
	require.Equal(t, filepath.ToSlash(filepath.Clean(sourcePath)), w.Header().Get("X-Agentland-File-Path"))
}
