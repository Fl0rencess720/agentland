package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type AgentSessionHandler struct {
	agentCoreClient    pb.AgentCoreServiceClient
	sessionStore       SessionStore
	tokenSigner        TokenSigner
	proxyEngine        *ProxyEngine
	defaultRuntimeName string
	defaultRuntimeNS   string
}

// InitAgentSessionApi 注册路由并在内部完成 Handler 字段的初始化
func InitAgentSessionApi(group *gin.RouterGroup, cfg *config.Config) {
	client, err := BuildAgentCoreClient(viper.GetString("agentcore.address"))
	if err != nil {
		zap.L().Error("Init AgentSession CoreClient failed", zap.Error(err))
		return
	}

	signer, err := BuildTokenSigner(cfg)
	if err != nil {
		zap.L().Error("Init AgentSession TokenSigner failed", zap.Error(err))
		return
	}

	h := &AgentSessionHandler{
		agentCoreClient:    client,
		sessionStore:       db.NewSessionStore(),
		tokenSigner:        signer,
		proxyEngine:        NewProxyEngine(),
		defaultRuntimeName: cfg.DefaultAgentRuntimeName,
		defaultRuntimeNS:   cfg.DefaultAgentRuntimeNamespace,
	}

	group.POST("/invocations/*path", h.Invoke)
	group.GET("/invocations/*path", h.Invoke)
	group.Any("/:sessionId/endpoints/by-port/:port", h.ProxyByPort)
	group.Any("/:sessionId/endpoints/by-port/:port/*path", h.ProxyByPort)
}

func (h *AgentSessionHandler) Invoke(ctx *gin.Context) {
	bodyBytes, ok := readRequestBody(ctx)
	if !ok {
		return
	}

	sandboxInfo, sessionID, err := h.resolveOrCreateSession(ctx)
	if err != nil {
		zap.L().Error("Resolve agent session failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	invokePath := ctx.Param("path")
	if invokePath == "" {
		invokePath = "/"
	}

	h.forwardRequest(ctx, sessionID, sandboxInfo, ctx.Request.Method, invokePath, bodyBytes)
}

func (h *AgentSessionHandler) ProxyByPort(ctx *gin.Context) {
	port := strings.TrimSpace(ctx.Param("port"))
	sessionID := strings.TrimSpace(ctx.Param("sessionId"))

	if port == "" || sessionID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "port and sessionId are required"})
		return
	}

	bodyBytes, ok := readRequestBody(ctx)
	if !ok {
		return
	}

	sandboxInfo, err := h.sessionStore.GetSession(ctx.Request.Context(), sessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		zap.L().Error("Get session from store failed", zap.String("sessionID", sessionID), zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	internalPath := "/api/proxy/by-port/" + port
	if subPath := ctx.Param("path"); subPath != "" {
		internalPath += subPath
	}

	h.forwardRequest(ctx, sessionID, sandboxInfo, ctx.Request.Method, internalPath, bodyBytes)
}

func (h *AgentSessionHandler) forwardRequest(ctx *gin.Context, sessionID string, sandboxInfo *db.SandboxInfo, method, path string, body []byte) {
	reqCtx, requestID := initRequestContext(ctx)
	ctx.Writer.Header().Set(SessionHeader, sessionID)

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
		zap.L().Error("Parse sandbox target failed", zap.Error(err))
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

func (h *AgentSessionHandler) resolveOrCreateSession(ctx *gin.Context) (*db.SandboxInfo, string, error) {
	sessionID := ctx.GetHeader(SessionHeader)
	reqCtx := ctx.Request.Context()

	if sessionID != "" {
		sandboxInfo, err := h.sessionStore.GetSession(reqCtx, sessionID)
		if err == nil {
			return sandboxInfo, sessionID, nil
		}
		if !errors.Is(err, db.ErrSessionNotFound) {
			return nil, "", fmt.Errorf("get session failed: %w", err)
		}
		zap.L().Warn("Session not found, creating new agent session", zap.String("sessionID", sessionID))
	}

	runtimeName, runtimeNamespace := resolveRuntimeRef(ctx, h.defaultRuntimeName, h.defaultRuntimeNS)
	if strings.TrimSpace(runtimeName) == "" {
		return nil, "", errors.New("runtime name is required")
	}

	createResp, err := h.agentCoreClient.CreateAgentSession(reqCtx, &pb.CreateAgentSessionRequest{
		RuntimeName:      runtimeName,
		RuntimeNamespace: runtimeNamespace,
	})
	if err != nil {
		return nil, "", fmt.Errorf("create agent session failed: %w", err)
	}

	info := &db.SandboxInfo{
		SandboxID:    createResp.SessionId,
		GrpcEndpoint: createResp.GrpcEndpoint,
	}
	return info, createResp.SessionId, nil
}

func resolveRuntimeRef(ctx *gin.Context, defaultName, defaultNS string) (string, string) {
	name := strings.TrimSpace(ctx.GetHeader("x-agentland-runtime"))
	if name == "" {
		name = strings.TrimSpace(ctx.Query("runtime"))
	}
	if name == "" {
		name = defaultName
	}

	ns := strings.TrimSpace(ctx.GetHeader("x-agentland-runtime-namespace"))
	if ns == "" {
		ns = strings.TrimSpace(ctx.Query("runtime_namespace"))
	}
	if ns == "" {
		ns = defaultNS
	}
	return name, ns
}
