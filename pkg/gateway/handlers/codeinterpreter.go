package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

type CodeInterpreterHandler struct {
	agentCoreClient pb.AgentCoreServiceClient
	sessionStore    SessionStore
	tokenSigner     TokenSigner
	proxyEngine     *ProxyEngine
}

type CreateSandboxResp struct {
	SandboxID string `json:"sandbox_id"`
}

// InitCodeInterpreterApi 注册路由并在内部完成 Handler 字段的初始化
func InitCodeInterpreterApi(group *gin.RouterGroup, cfg *config.Config) {
	client, err := BuildAgentCoreClient(viper.GetString("agentcore.address"))
	if err != nil {
		zap.L().Error("Init CodeInterpreter CoreClient failed", zap.Error(err))
		return
	}

	signer, err := BuildTokenSigner(cfg)
	if err != nil {
		zap.L().Error("Init CodeInterpreter TokenSigner failed", zap.Error(err))
		return
	}

	h := &CodeInterpreterHandler{
		agentCoreClient: client,
		sessionStore:    db.NewSessionStore(),
		tokenSigner:     signer,
		proxyEngine:     NewProxyEngine(),
	}

	group.POST("/sandboxes", h.CreateSandbox)
	group.POST("/contexts", h.CreateContext)
	group.POST("/contexts/:contextId/execute", h.ExecuteInContext)
	group.GET("/contexts/:contextId/executions/:executionId/output", h.GetExecutionOutput)
	group.DELETE("/contexts/:contextId", h.DeleteContext)

	group.GET("/fs/tree", h.GetFSTree)
	group.GET("/fs/file", h.GetFSFile)
	group.POST("/fs/file", h.WriteFSFile)
	group.POST("/fs/upload", h.UploadFSFile)
	group.GET("/fs/download", h.DownloadFSFile)
}

func (h *CodeInterpreterHandler) CreateSandbox(ctx *gin.Context) {
	reqCtx, requestID := initRequestContext(ctx)

	tracer := otel.Tracer("gateway.codeinterpreter")
	reqCtx, span := tracer.Start(reqCtx, "gateway.codeinterpreter.create_rpc")
	defer span.End()

	if requestID != "" {
		reqCtx = metadata.AppendToOutgoingContext(reqCtx, observability.RequestIDHeader, requestID)
		span.SetAttributes(attribute.String("request.id", requestID))
	}

	resp, err := h.agentCoreClient.CreateCodeInterpreter(reqCtx, &pb.CreateSandboxRequest{})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create codeinterpreter rpc failed")
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	span.SetAttributes(attribute.String("agentland.session_id", resp.SandboxId))

	if err := h.sessionStore.UpdateLatestActivity(reqCtx, resp.SandboxId); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sandboxID", resp.SandboxId), zap.Error(err))
	}

	response.SuccessResponse(ctx, CreateSandboxResp{SandboxID: resp.SandboxId})
}

func (h *CodeInterpreterHandler) CreateContext(ctx *gin.Context) {
	var req models.CreateContextReq
	bodyBytes, ok := bindJSONWithBody(ctx, &req)
	if !ok || !isSupportedCodeLanguage(req.Language) {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodPost, "/api/contexts", bodyBytes)
}

func (h *CodeInterpreterHandler) ExecuteInContext(ctx *gin.Context) {
	contextID := strings.TrimSpace(ctx.Param("contextId"))
	if contextID == "" {
		writeSSEError(ctx, contextID, "context_id is required")
		return
	}

	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		writeSSEError(ctx, contextID, "read request body failed")
		return
	}
	ctx.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var req models.ExecuteContextReq
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeSSEError(ctx, contextID, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		writeSSEError(ctx, contextID, "code is required")
		return
	}
	if req.TimeoutMs != 0 && (req.TimeoutMs < 100 || req.TimeoutMs > 300000) {
		writeSSEError(ctx, contextID, "timeout_ms must be between 100 and 300000")
		return
	}

	// Force SSE transport for code execution.
	ctx.Request.Header.Set("Accept", "text/event-stream")
	h.forwardToSandboxSSE(ctx, http.MethodPost, "/api/contexts/"+contextID+"/execute", bodyBytes, contextID)
}

func (h *CodeInterpreterHandler) DeleteContext(ctx *gin.Context) {
	contextID := strings.TrimSpace(ctx.Param("contextId"))
	if contextID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodDelete, "/api/contexts/"+contextID, nil)
}

func (h *CodeInterpreterHandler) GetExecutionOutput(ctx *gin.Context) {
	contextID := strings.TrimSpace(ctx.Param("contextId"))
	executionID := strings.TrimSpace(ctx.Param("executionId"))
	if contextID == "" || executionID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodGet, "/api/contexts/"+contextID+"/executions/"+executionID+"/output", nil)
}

func (h *CodeInterpreterHandler) GetFSTree(ctx *gin.Context) {
	h.forwardToSandbox(ctx, ctx.Request.Method, "/api/fs/tree", nil)
}

func (h *CodeInterpreterHandler) GetFSFile(ctx *gin.Context) {
	h.forwardToSandbox(ctx, ctx.Request.Method, "/api/fs/file", nil)
}

func (h *CodeInterpreterHandler) WriteFSFile(ctx *gin.Context) {
	var req models.WriteFSFileReq
	bodyBytes, ok := bindJSONWithBody(ctx, &req)
	if !ok || strings.TrimSpace(req.Path) == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodPost, "/api/fs/file", bodyBytes)
}

func (h *CodeInterpreterHandler) UploadFSFile(ctx *gin.Context) {
	contentType := strings.ToLower(strings.TrimSpace(ctx.GetHeader("Content-Type")))
	if !strings.HasPrefix(contentType, "multipart/form-data") {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodPost, "/api/fs/upload", nil)
}

func (h *CodeInterpreterHandler) DownloadFSFile(ctx *gin.Context) {
	if strings.TrimSpace(ctx.Query("path")) == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodGet, "/api/fs/download", nil)
}

func (h *CodeInterpreterHandler) forwardToSandbox(ctx *gin.Context, method, path string, body []byte) {
	sessionID := strings.TrimSpace(ctx.GetHeader(SessionHeader))
	if sessionID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	reqCtx, requestID := initRequestContext(ctx)
	ctx.Writer.Header().Set(SessionHeader, sessionID)

	sandboxInfo, err := h.sessionStore.GetSession(reqCtx, sessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		zap.L().Error("Get session info failed", zap.String("sessionID", sessionID), zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sessionID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sessionID", sessionID), zap.Error(err))
	}

	token, err := h.tokenSigner.Sign(sessionID, "", 0)
	if err != nil {
		zap.L().Error("Issue sandbox token failed", zap.String("sessionID", sessionID), zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		zap.L().Error("Parse sandbox url failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	h.proxyEngine.Forward(ctx, ProxyConfig{
		Target:       target,
		Method:       method,
		InternalPath: path,
		Body:         body,
		SessionID:    sessionID,
		SandboxToken: token,
		RequestID:    requestID,
	})
}

func setupSSEResponse(ctx *gin.Context) {
	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Connection", "keep-alive")
	ctx.Header("X-Accel-Buffering", "no")
	ctx.Status(http.StatusOK)
	if flusher, ok := ctx.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeSSE(ctx *gin.Context, mu *sync.Mutex, evt models.ExecuteStreamEvent) bool {
	if ctx == nil {
		return false
	}
	b, err := json.Marshal(evt)
	if err != nil {
		return false
	}

	mu.Lock()
	defer mu.Unlock()

	select {
	case <-ctx.Request.Context().Done():
		return false
	default:
	}

	if _, err := ctx.Writer.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
		return false
	}
	if flusher, ok := ctx.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
}

func writeSSEError(ctx *gin.Context, contextID, message string) {
	setupSSEResponse(ctx)
	var mu sync.Mutex
	_ = writeSSE(ctx, &mu, models.ExecuteStreamEvent{
		Type:      "error",
		Timestamp: time.Now().UnixMilli(),
		ContextID: contextID,
		Error:     message,
	})
}

func (h *CodeInterpreterHandler) forwardToSandboxSSE(
	ctx *gin.Context,
	method, path string,
	body []byte,
	contextID string,
) {
	sessionID := strings.TrimSpace(ctx.GetHeader(SessionHeader))
	if sessionID == "" {
		writeSSEError(ctx, contextID, "x-agentland-session is required")
		return
	}

	reqCtx, requestID := initRequestContext(ctx)
	ctx.Writer.Header().Set(SessionHeader, sessionID)

	sandboxInfo, err := h.sessionStore.GetSession(reqCtx, sessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			writeSSEError(ctx, contextID, "session not found")
			return
		}
		zap.L().Error("Get session info failed", zap.String("sessionID", sessionID), zap.Error(err))
		writeSSEError(ctx, contextID, "get session info failed")
		return
	}

	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sessionID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sessionID", sessionID), zap.Error(err))
	}

	token, err := h.tokenSigner.Sign(sessionID, "", 0)
	if err != nil {
		zap.L().Error("Issue sandbox token failed", zap.String("sessionID", sessionID), zap.Error(err))
		writeSSEError(ctx, contextID, "issue sandbox token failed")
		return
	}

	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		zap.L().Error("Parse sandbox url failed", zap.Error(err))
		writeSSEError(ctx, contextID, "parse sandbox url failed")
		return
	}

	h.proxyEngine.Forward(ctx, ProxyConfig{
		Target:       target,
		Method:       method,
		InternalPath: path,
		Body:         body,
		SessionID:    sessionID,
		SandboxToken: token,
		RequestID:    requestID,
	})
}
