package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	gwhandlers "github.com/Fl0rencess720/agentland/pkg/gateway/handlers"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodeInterpreterToolBridge_CreateSandbox(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.POST("/api/code-runner/sandboxes", func(c *gin.Context) {
		require.Equal(t, "application/json", c.GetHeader("Content-Type"))

		var req gwhandlers.CreateSandboxReq
		require.NoError(t, c.ShouldBindJSON(&req))
		require.Equal(t, "python", req.Language)

		c.JSON(http.StatusOK, gin.H{
			"msg":  "success",
			"code": 200,
			"data": sandboxCreateToolOutput{
				SandboxID: "sid-created",
			},
		})
	})

	bridge := &codeInterpreterToolBridge{router: router}
	result, out, err := bridge.createSandbox(context.Background(), nil, sandboxCreateToolInput{
		Language: "python",
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "sid-created", out.SandboxID)
}

func TestCodeInterpreterToolBridge_ExecuteCode_AsyncDeleteContext(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	deleteCalled := make(chan struct{}, 1)

	router := gin.New()
	router.POST("/api/code-runner/contexts", func(c *gin.Context) {
		require.Equal(t, "sid-exec", c.GetHeader(gwhandlers.SessionHeader))
		require.Equal(t, "application/json", c.GetHeader("Content-Type"))

		var req gwhandlers.CreateContextReq
		require.NoError(t, c.ShouldBindJSON(&req))
		require.Equal(t, "python", req.Language)
		require.Equal(t, "/workspace", req.CWD)

		c.JSON(http.StatusOK, gin.H{
			"msg":  "success",
			"code": 200,
			"data": createContextToolOutput{
				ContextID: "ctx-1",
			},
		})
	})

	router.POST("/api/code-runner/contexts/ctx-1/execute", func(c *gin.Context) {
		require.Equal(t, "sid-exec", c.GetHeader(gwhandlers.SessionHeader))
		require.Equal(t, "application/json", c.GetHeader("Content-Type"))

		var req gwhandlers.ExecuteInContextReq
		require.NoError(t, c.ShouldBindJSON(&req))
		require.Equal(t, "print(1)", req.Code)
		require.Equal(t, 30000, req.TimeoutMs)

		c.JSON(http.StatusOK, gin.H{
			"msg":  "success",
			"code": 200,
			"data": codeExecuteToolOutput{
				ContextID:      "ctx-1",
				ExecutionCount: 1,
				ExitCode:       0,
				Stdout:         "1\n",
				Stderr:         "",
				DurationMs:     5,
			},
		})
	})

	router.DELETE("/api/code-runner/contexts/ctx-1", func(c *gin.Context) {
		require.Equal(t, "sid-exec", c.GetHeader(gwhandlers.SessionHeader))
		select {
		case deleteCalled <- struct{}{}:
		default:
		}
		c.JSON(http.StatusOK, gin.H{"msg": "success", "code": 200, "data": gin.H{"context_id": "ctx-1"}})
	})

	bridge := &codeInterpreterToolBridge{router: router}
	result, out, err := bridge.executeCode(context.Background(), nil, codeExecuteToolInput{
		SandboxID: "sid-exec",
		Language:  "python",
		CWD:       "/workspace",
		Code:      "print(1)",
		TimeoutMs: 30000,
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, int32(0), out.ExitCode)
	require.Equal(t, "1\n", out.Stdout)

	select {
	case <-deleteCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("expected async context deletion to be triggered")
	}
}

func TestDecodeProxyData_RawJSONBody(t *testing.T) {
	rec := httptestJSON(http.StatusOK, gin.H{
		"context_id": "ctx-raw",
		"exit_code":  0,
		"stdout":     "ok",
	})

	out, err := decodeProxyData[codeExecuteToolOutput](rec)
	require.NoError(t, err)
	require.Equal(t, "ctx-raw", out.ContextID)
	require.Equal(t, int32(0), out.ExitCode)
	require.Equal(t, "ok", out.Stdout)
}

func TestCodeInterpreterToolBridge_GetFSTree(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.GET("/api/code-runner/fs/tree", func(c *gin.Context) {
		require.Equal(t, "sid-tree", c.GetHeader(gwhandlers.SessionHeader))
		require.Equal(t, "/workspace", c.Query("path"))
		require.Equal(t, "2", c.Query("depth"))
		require.Equal(t, "true", c.Query("includeHidden"))

		c.JSON(http.StatusOK, gin.H{
			"msg":  "success",
			"code": 200,
			"data": models.GetFSTreeResp{
				Root: "/workspace",
				Nodes: []models.FSTreeNode{
					{Path: "a.txt", Name: "a.txt", Type: "file", Size: 3},
				},
			},
		})
	})

	bridge := &codeInterpreterToolBridge{router: router}
	result, out, err := bridge.getFSTree(context.Background(), nil, fsTreeToolInput{
		SandboxID:     "sid-tree",
		Path:          "/workspace",
		Depth:         2,
		IncludeHidden: true,
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "/workspace", out.Root)
	require.Len(t, out.Nodes, 1)
	require.Equal(t, "a.txt", out.Nodes[0].Path)
}

func TestCodeInterpreterToolBridge_GetFSFile(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.GET("/api/code-runner/fs/file", func(c *gin.Context) {
		require.Equal(t, "sid-read", c.GetHeader(gwhandlers.SessionHeader))
		require.Equal(t, "/workspace/a.txt", c.Query("path"))
		require.Equal(t, "utf8", c.Query("encoding"))

		c.JSON(http.StatusOK, gin.H{
			"msg":  "success",
			"code": 200,
			"data": models.GetFSFileResp{
				Path:     "/workspace/a.txt",
				Size:     3,
				Encoding: "utf8",
				Content:  "abc",
			},
		})
	})

	bridge := &codeInterpreterToolBridge{router: router}
	result, out, err := bridge.getFSFile(context.Background(), nil, fsGetFileToolInput{
		SandboxID: "sid-read",
		Path:      "/workspace/a.txt",
		Encoding:  "utf8",
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "abc", out.Content)
}

func TestCodeInterpreterToolBridge_WriteFSFile(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.POST("/api/code-runner/fs/file", func(c *gin.Context) {
		require.Equal(t, "sid-write", c.GetHeader(gwhandlers.SessionHeader))
		require.Equal(t, "application/json", c.GetHeader("Content-Type"))

		var req models.WriteFSFileReq
		require.NoError(t, c.ShouldBindJSON(&req))
		require.Equal(t, "/workspace/data.txt", req.Path)
		require.Equal(t, "line1\nline2", req.Content)
		require.Equal(t, "utf8", req.Encoding)

		c.JSON(http.StatusOK, gin.H{
			"msg":  "success",
			"code": 200,
			"data": models.WriteFSFileResp{
				Path:     req.Path,
				Size:     int64(len(req.Content)),
				Encoding: req.Encoding,
			},
		})
	})

	bridge := &codeInterpreterToolBridge{router: router}
	result, out, err := bridge.writeFSFile(context.Background(), nil, fsWriteFileToolInput{
		SandboxID: "sid-write",
		Path:      "/workspace/data.txt",
		Content:   "line1\nline2",
		Encoding:  "utf8",
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, "/workspace/data.txt", out.Path)
}

func TestCodeInterpreterToolBridge_MissingSandboxID(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	bridge := &codeInterpreterToolBridge{router: router}

	result, _, err := bridge.getFSFile(context.Background(), nil, fsGetFileToolInput{
		Path:     "/workspace/a.txt",
		Encoding: "utf8",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox_id is required")
	require.Nil(t, result)
}

func TestDecodeSuccessData_BusinessError(t *testing.T) {
	rec := httptestJSON(http.StatusOK, gin.H{
		"msg":  "failed",
		"code": 500,
		"data": gin.H{},
	})

	_, err := decodeSuccessData[models.GetFSFileResp](rec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gateway business code=500")
}

func httptestJSON(status int, body any) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	data, _ := json.Marshal(body)
	rec.Code = status
	rec.Body = bytes.NewBuffer(data)
	return rec
}
