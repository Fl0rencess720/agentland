package handlers

import (
	"errors"
	"net/http"
	"strings"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
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

type CreateSandboxReq struct {
	Language string `json:"language"`
}

type CreateSandboxResp struct {
	SandboxID string `json:"sandbox_id"`
}

type CreateContextReq struct {
	Language string `json:"language"`
	CWD      string `json:"cwd,omitempty"`
}

type ExecuteInContextReq struct {
	Code      string `json:"code"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type WriteFSFileReq struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
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
	group.DELETE("/contexts/:contextId", h.DeleteContext)

	group.GET("/fs/tree", h.GetFSTree)
	group.GET("/fs/file", h.GetFSFile)
	group.POST("/fs/file", h.WriteFSFile)
	group.POST("/fs/upload", h.UploadFSFile)
	group.GET("/fs/download", h.DownloadFSFile)
}

func (h *CodeInterpreterHandler) CreateSandbox(ctx *gin.Context) {
	reqCtx, requestID := initRequestContext(ctx)

	var req CreateSandboxReq
	if err := ctx.ShouldBindJSON(&req); err != nil || req.Language != LanguagePython {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	tracer := otel.Tracer("gateway.codeinterpreter")
	reqCtx, span := tracer.Start(reqCtx, "gateway.codeinterpreter.create_rpc")
	defer span.End()

	if requestID != "" {
		reqCtx = metadata.AppendToOutgoingContext(reqCtx, observability.RequestIDHeader, requestID)
		span.SetAttributes(attribute.String("request.id", requestID))
	}
	span.SetAttributes(attribute.String("sandbox.language", req.Language))

	resp, err := h.agentCoreClient.CreateCodeInterpreter(reqCtx, &pb.CreateSandboxRequest{Language: req.Language})
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
	var req CreateContextReq
	bodyBytes, ok := bindJSONWithBody(ctx, &req)
	if !ok || req.Language != LanguagePython {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodPost, "/api/contexts", bodyBytes)
}

func (h *CodeInterpreterHandler) ExecuteInContext(ctx *gin.Context) {
	var req ExecuteInContextReq
	bodyBytes, ok := bindJSONWithBody(ctx, &req)
	if !ok || strings.TrimSpace(req.Code) == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	if req.TimeoutMs != 0 && (req.TimeoutMs < 100 || req.TimeoutMs > 300000) {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	contextID := ctx.Param("contextId")
	if contextID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodPost, "/api/contexts/"+contextID+"/execute", bodyBytes)
}

func (h *CodeInterpreterHandler) DeleteContext(ctx *gin.Context) {
	contextID := strings.TrimSpace(ctx.Param("contextId"))
	if contextID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	h.forwardToSandbox(ctx, http.MethodDelete, "/api/contexts/"+contextID, nil)
}

func (h *CodeInterpreterHandler) GetFSTree(ctx *gin.Context) {
	h.forwardToSandbox(ctx, ctx.Request.Method, "/api/fs/tree", nil)
}

func (h *CodeInterpreterHandler) GetFSFile(ctx *gin.Context) {
	h.forwardToSandbox(ctx, ctx.Request.Method, "/api/fs/file", nil)
}

func (h *CodeInterpreterHandler) WriteFSFile(ctx *gin.Context) {
	var req WriteFSFileReq
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
