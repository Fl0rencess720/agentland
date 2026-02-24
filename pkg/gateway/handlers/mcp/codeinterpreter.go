package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	gwhandlers "github.com/Fl0rencess720/agentland/pkg/gateway/handlers"
	"github.com/gin-gonic/gin"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

type codeInterpreterToolBridge struct {
	router http.Handler
}

type fsTreeToolInput struct {
	SandboxID     string `json:"sandbox_id" jsonschema:"Sandbox session id from create sandbox API"`
	Path          string `json:"path" jsonschema:"Directory path to traverse, relative or absolute"`
	Depth         int    `json:"depth,omitempty" jsonschema:"Traversal depth, valid range is 1-20"`
	IncludeHidden bool   `json:"includeHidden,omitempty" jsonschema:"Whether to include hidden files and directories"`
}

type fsGetFileToolInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"Sandbox session id from create sandbox API"`
	Path      string `json:"path" jsonschema:"File path to read, relative or absolute"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"Content encoding, supported values: utf8, utf-8, base64"`
}

type fsWriteFileToolInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"Sandbox session id from create sandbox API"`
	Path      string `json:"path" jsonschema:"Destination file path, relative or absolute"`
	Content   string `json:"content" jsonschema:"File content to write"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"Input content encoding, supported values: utf8, utf-8, base64"`
}

type sandboxCreateToolInput struct {
	Language string `json:"language,omitempty" jsonschema:"Sandbox language, supported values: python, shell. Defaults to python"`
}

type sandboxCreateToolOutput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"Created sandbox session id"`
}

type codeExecuteToolInput struct {
	SandboxID string `json:"sandbox_id" jsonschema:"Sandbox session id from create sandbox API"`
	Language  string `json:"language,omitempty" jsonschema:"Context language, supported values: python, shell. Defaults to python"`
	CWD       string `json:"cwd,omitempty" jsonschema:"Working directory for the temporary context"`
	Code      string `json:"code" jsonschema:"Code to execute in the temporary context"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"Execution timeout in milliseconds, valid range is 100-300000"`
}

type createContextToolOutput struct {
	ContextID string `json:"context_id"`
}

type codeExecuteToolOutput struct {
	ContextID      string `json:"context_id"`
	ExecutionCount int64  `json:"execution_count"`
	ExitCode       int32  `json:"exit_code"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	DurationMs     int64  `json:"duration_ms"`
}

func registerCodeInterpreterTools(server *sdkmcp.Server, cfg *config.Config) {
	bridge := newCodeInterpreterToolBridge(cfg)

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "sandbox_create",
		Description: "Create a code runner sandbox session",
	}, bridge.createSandbox)

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "code_execute",
		Description: "Execute code once in a temporary context that is deleted asynchronously after execution",
	}, bridge.executeCode)

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "fs_tree",
		Description: "List files and directories under a path",
	}, bridge.getFSTree)

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "fs_file_get",
		Description: "Read file content with utf8 or base64 encoding",
	}, bridge.getFSFile)

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "fs_file_write",
		Description: "Write file content with utf8 or base64 encoding",
	}, bridge.writeFSFile)
}

func newCodeInterpreterToolBridge(cfg *config.Config) *codeInterpreterToolBridge {
	engine := gin.New()
	api := engine.Group("/api")
	gwhandlers.InitCodeInterpreterApi(api.Group("/code-runner"), cfg)
	return &codeInterpreterToolBridge{router: engine}
}

func (b *codeInterpreterToolBridge) createSandbox(_ context.Context, _ *sdkmcp.CallToolRequest, in sandboxCreateToolInput) (*sdkmcp.CallToolResult, sandboxCreateToolOutput, error) {
	language := strings.ToLower(strings.TrimSpace(in.Language))
	if language == "" {
		language = "python"
	}

	payload, err := json.Marshal(gwhandlers.CreateSandboxReq{Language: language})
	if err != nil {
		return nil, sandboxCreateToolOutput{}, fmt.Errorf("marshal create sandbox req: %w", err)
	}

	rec, err := b.invokeWithoutSession(http.MethodPost, "/api/code-runner/sandboxes", "application/json", payload)
	if err != nil {
		return nil, sandboxCreateToolOutput{}, err
	}
	out, err := decodeSuccessData[sandboxCreateToolOutput](rec)
	if err != nil {
		return nil, sandboxCreateToolOutput{}, err
	}
	return nil, out, nil
}

func (b *codeInterpreterToolBridge) executeCode(_ context.Context, _ *sdkmcp.CallToolRequest, in codeExecuteToolInput) (*sdkmcp.CallToolResult, codeExecuteToolOutput, error) {
	code := strings.TrimSpace(in.Code)
	if code == "" {
		return nil, codeExecuteToolOutput{}, fmt.Errorf("code is required")
	}

	language := strings.ToLower(strings.TrimSpace(in.Language))
	if language == "" {
		language = "python"
	}

	createPayload, err := json.Marshal(gwhandlers.CreateContextReq{
		Language: language,
		CWD:      in.CWD,
	})
	if err != nil {
		return nil, codeExecuteToolOutput{}, fmt.Errorf("marshal create context req: %w", err)
	}

	createRec, err := b.invoke(http.MethodPost, "/api/code-runner/contexts", in.SandboxID, "application/json", createPayload)
	if err != nil {
		return nil, codeExecuteToolOutput{}, err
	}
	createOut, err := decodeProxyData[createContextToolOutput](createRec)
	if err != nil {
		return nil, codeExecuteToolOutput{}, err
	}

	contextID := strings.TrimSpace(createOut.ContextID)
	if contextID == "" {
		return nil, codeExecuteToolOutput{}, fmt.Errorf("create context returned empty context_id")
	}
	defer b.deleteContextAsync(in.SandboxID, contextID)

	execPayload, err := json.Marshal(gwhandlers.ExecuteInContextReq{
		Code:      in.Code,
		TimeoutMs: in.TimeoutMs,
	})
	if err != nil {
		return nil, codeExecuteToolOutput{}, fmt.Errorf("marshal execute code req: %w", err)
	}

	execPath := "/api/code-runner/contexts/" + url.PathEscape(contextID) + "/execute"
	execRec, err := b.invoke(http.MethodPost, execPath, in.SandboxID, "application/json", execPayload)
	if err != nil {
		return nil, codeExecuteToolOutput{}, err
	}
	execOut, err := decodeProxyData[codeExecuteToolOutput](execRec)
	if err != nil {
		return nil, codeExecuteToolOutput{}, err
	}
	if strings.TrimSpace(execOut.ContextID) == "" {
		execOut.ContextID = contextID
	}

	return nil, execOut, nil
}

func (b *codeInterpreterToolBridge) getFSTree(_ context.Context, _ *sdkmcp.CallToolRequest, in fsTreeToolInput) (*sdkmcp.CallToolResult, models.GetFSTreeResp, error) {
	query := url.Values{}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		path = "."
	}
	query.Set("path", path)
	if in.Depth > 0 {
		query.Set("depth", strconv.Itoa(in.Depth))
	}
	if in.IncludeHidden {
		query.Set("includeHidden", "true")
	}

	rec, err := b.invoke(http.MethodGet, "/api/code-runner/fs/tree?"+query.Encode(), in.SandboxID, "", nil)
	if err != nil {
		return nil, models.GetFSTreeResp{}, err
	}
	out, err := decodeSuccessData[models.GetFSTreeResp](rec)
	if err != nil {
		return nil, models.GetFSTreeResp{}, err
	}
	return nil, out, nil
}

func (b *codeInterpreterToolBridge) getFSFile(_ context.Context, _ *sdkmcp.CallToolRequest, in fsGetFileToolInput) (*sdkmcp.CallToolResult, models.GetFSFileResp, error) {
	query := url.Values{}
	query.Set("path", strings.TrimSpace(in.Path))
	if enc := strings.TrimSpace(in.Encoding); enc != "" {
		query.Set("encoding", enc)
	}

	rec, err := b.invoke(http.MethodGet, "/api/code-runner/fs/file?"+query.Encode(), in.SandboxID, "", nil)
	if err != nil {
		return nil, models.GetFSFileResp{}, err
	}
	out, err := decodeSuccessData[models.GetFSFileResp](rec)
	if err != nil {
		return nil, models.GetFSFileResp{}, err
	}
	return nil, out, nil
}

func (b *codeInterpreterToolBridge) writeFSFile(_ context.Context, _ *sdkmcp.CallToolRequest, in fsWriteFileToolInput) (*sdkmcp.CallToolResult, models.WriteFSFileResp, error) {
	payload, err := json.Marshal(models.WriteFSFileReq{
		Path:     in.Path,
		Content:  in.Content,
		Encoding: in.Encoding,
	})
	if err != nil {
		return nil, models.WriteFSFileResp{}, fmt.Errorf("marshal write req: %w", err)
	}

	rec, err := b.invoke(http.MethodPost, "/api/code-runner/fs/file", in.SandboxID, "application/json", payload)
	if err != nil {
		return nil, models.WriteFSFileResp{}, err
	}
	out, err := decodeSuccessData[models.WriteFSFileResp](rec)
	if err != nil {
		return nil, models.WriteFSFileResp{}, err
	}
	return nil, out, nil
}

func (b *codeInterpreterToolBridge) invoke(method, rawPath, sandboxID, contentType string, body []byte) (*httptest.ResponseRecorder, error) {
	sid := strings.TrimSpace(sandboxID)
	if sid == "" {
		return nil, fmt.Errorf("sandbox_id is required")
	}

	req := httptest.NewRequest(method, rawPath, bytes.NewReader(body))
	req.Header.Set(gwhandlers.SessionHeader, sid)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	b.router.ServeHTTP(rec, req)
	return rec, nil
}

func (b *codeInterpreterToolBridge) invokeWithoutSession(method, rawPath, contentType string, body []byte) (*httptest.ResponseRecorder, error) {
	req := httptest.NewRequest(method, rawPath, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	b.router.ServeHTTP(rec, req)
	return rec, nil
}

func (b *codeInterpreterToolBridge) deleteContextAsync(sandboxID, contextID string) {
	go func() {
		path := "/api/code-runner/contexts/" + url.PathEscape(contextID)
		rec, err := b.invoke(http.MethodDelete, path, sandboxID, "", nil)
		if err != nil {
			zap.L().Warn("mcp async delete context failed", zap.String("sandbox_id", sandboxID), zap.String("context_id", contextID), zap.Error(err))
			return
		}
		if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
			zap.L().Warn("mcp async delete context non-2xx",
				zap.String("sandbox_id", sandboxID),
				zap.String("context_id", contextID),
				zap.Int("http_code", rec.Code),
				zap.String("body", strings.TrimSpace(rec.Body.String())),
			)
		}
	}()
}

type successEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func decodeSuccessData[T any](rec *httptest.ResponseRecorder) (T, error) {
	var zero T
	if rec.Code != http.StatusOK {
		return zero, decodeHTTPError(rec)
	}

	var envelope successEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		return zero, fmt.Errorf("decode success envelope: %w", err)
	}
	if envelope.Code != http.StatusOK {
		return zero, fmt.Errorf("gateway business code=%d msg=%s", envelope.Code, envelope.Msg)
	}

	var out T
	if err := json.Unmarshal(envelope.Data, &out); err != nil {
		return zero, fmt.Errorf("decode success data: %w", err)
	}
	return out, nil
}

type proxyEnvelope struct {
	Code *int            `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func decodeProxyData[T any](rec *httptest.ResponseRecorder) (T, error) {
	var zero T
	if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
		return zero, decodeHTTPError(rec)
	}

	body := bytes.TrimSpace(rec.Body.Bytes())
	if len(body) == 0 {
		return zero, fmt.Errorf("empty response body")
	}

	var envelope proxyEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Code != nil {
		if *envelope.Code != http.StatusOK {
			return zero, fmt.Errorf("gateway business code=%d msg=%s", *envelope.Code, envelope.Msg)
		}
		if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
			return zero, fmt.Errorf("gateway success data is empty")
		}

		var out T
		if err := json.Unmarshal(envelope.Data, &out); err != nil {
			return zero, fmt.Errorf("decode success data: %w", err)
		}
		return out, nil
	}

	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, fmt.Errorf("decode response body: %w", err)
	}
	return out, nil
}

func decodeHTTPError(rec *httptest.ResponseRecorder) error {
	body := strings.TrimSpace(rec.Body.String())
	if body == "" {
		return fmt.Errorf("gateway http=%d", rec.Code)
	}
	return fmt.Errorf("gateway http=%d body=%s", rec.Code, body)
}
