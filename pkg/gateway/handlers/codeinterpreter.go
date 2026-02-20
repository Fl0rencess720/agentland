package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/Fl0rencess720/agentland/pkg/common/sandboxtoken"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

type CodeInterpreterHandler struct {
	agentCoreServiceClient pb.AgentCoreServiceClient
	sandboxTransport       http.RoundTripper
	sessionStore           sessionStore
	sandboxTokenSigner     tokenSigner
}

type sessionStore interface {
	GetSession(ctx context.Context, sandboxID string) (*db.SandboxInfo, error)
	UpdateLatestActivity(ctx context.Context, sandboxID string) error
}

type tokenSigner interface {
	Sign(sessionID, subject string, version int64) (string, error)
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

type ExecuteContextReq struct {
	Code      string `json:"code"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type proxySandboxRequestOpts struct {
	target       *url.URL
	method       string
	path         string
	bodyBytes    []byte
	sandboxToken string
	sessionID    string
	requestID    string
}

const sessionHeader = "x-agentland-session"

func InitCodeInterpreterApi(group *gin.RouterGroup, cfg *config.Config) {
	h := &CodeInterpreterHandler{}

	kacp := keepalive.ClientParameters{
		Time:                10 * time.Second,
		Timeout:             time.Second,
		PermitWithoutStream: false,
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy": "round_robin"}`),
		grpc.WithKeepaliveParams(kacp),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	}

	address := viper.GetString("agentcore.address")

	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		zap.L().Error("Connect to service via Kubernetes DNS failed", zap.Error(err))
		return
	}

	h.sessionStore = db.NewSessionStore()

	h.sandboxTransport = &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

	signer, err := sandboxtoken.NewSignerFromConfig(sandboxtoken.SignerConfig{
		PrivateKeyPath: cfg.SandboxJWTPrivatePath,
		Issuer:         cfg.SandboxJWTIssuer,
		Audience:       cfg.SandboxJWTAudience,
		KID:            cfg.SandboxJWTKID,
		TTL:            cfg.SandboxJWTTTL,
	})
	if err != nil {
		zap.L().Error("Init sandbox token signer failed", zap.Error(err))
		return
	}
	h.sandboxTokenSigner = signer

	h.agentCoreServiceClient = pb.NewAgentCoreServiceClient(conn)

	// POST /code-runner/sandboxes 创建 CodeInterpreter 沙箱
	group.POST("/sandboxes", h.CreateSandbox)
	// POST /code-runner/contexts 在指定 sandbox 中创建可复用执行上下文
	group.POST("/contexts", h.CreateContext)
	// POST /code-runner/contexts/:contextId/execute 在 context 中执行代码
	group.POST("/contexts/:contextId/execute", h.ExecuteInContext)
	// DELETE /code-runner/contexts/:contextId 删除 context 并释放资源
	group.DELETE("/contexts/:contextId", h.DeleteContext)
}

func (h *CodeInterpreterHandler) CreateSandbox(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	requestID := observability.RequestIDFromContext(reqCtx)
	ctx.Writer.Header().Set(observability.RequestIDHeader, requestID)

	var req CreateSandboxReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	// 检查语言是否支持
	if req.Language != "python" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	// 调用 agentcore 创建沙箱
	sandboxInfo, err := h.createSandbox(reqCtx, req.Language)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	// 更新沙箱状态
	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sandboxInfo.SandboxID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sandboxID", sandboxInfo.SandboxID), zap.Error(err))
	}

	response.SuccessResponse(ctx, CreateSandboxResp{
		SandboxID: sandboxInfo.SandboxID,
	})
}

func (h *CodeInterpreterHandler) CreateContext(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	requestID := observability.RequestIDFromContext(reqCtx)
	ctx.Writer.Header().Set(observability.RequestIDHeader, requestID)

	// 读取请求体 Bytes
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	var req CreateContextReq
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	// 检查语言是否支持
	if req.Language != "python" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	// 从请求头获取 sessionID
	sessionID := ctx.GetHeader(sessionHeader)
	if sessionID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	// 从 Redis 获取沙箱信息
	sandboxInfo, err := h.sessionStore.GetSession(reqCtx, sessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			response.ErrorResponse(ctx, response.FormError)
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	// 更新沙箱状态
	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sandboxInfo.SandboxID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sandboxID", sandboxInfo.SandboxID), zap.Error(err))
	}

	// 认证
	sandboxToken, err := h.issueSandboxToken(sandboxInfo.SandboxID)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	// 解析反向代理请求目标
	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	// 把当前 sandbox 会话 ID 回写到响应头里
	ctx.Writer.Header().Set(sessionHeader, sandboxInfo.SandboxID)

	// 反向代理到沙箱
	h.proxySandboxRequest(ctx, proxySandboxRequestOpts{
		target:       target,
		method:       http.MethodPost,
		path:         "/api/contexts",
		bodyBytes:    bodyBytes,
		sandboxToken: sandboxToken,
		sessionID:    sandboxInfo.SandboxID,
		requestID:    requestID,
	})
}

func (h *CodeInterpreterHandler) ExecuteInContext(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	requestID := observability.RequestIDFromContext(reqCtx)
	ctx.Writer.Header().Set(observability.RequestIDHeader, requestID)

	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	var req ExecuteContextReq
	if err := json.Unmarshal(bodyBytes, &req); err != nil || strings.TrimSpace(req.Code) == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	if req.TimeoutMs != 0 && (req.TimeoutMs < 100 || req.TimeoutMs > 300000) {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	sessionID := ctx.GetHeader(sessionHeader)
	if sessionID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	sandboxInfo, err := h.sessionStore.GetSession(reqCtx, sessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			response.ErrorResponse(ctx, response.FormError)
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sessionID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sandboxID", sessionID), zap.Error(err))
	}

	sandboxToken, err := h.issueSandboxToken(sessionID)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	contextID := ctx.Param("contextId")
	if contextID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	ctx.Writer.Header().Set(sessionHeader, sessionID)

	h.proxySandboxRequest(ctx, proxySandboxRequestOpts{
		target:       target,
		method:       http.MethodPost,
		path:         "/api/contexts/" + contextID + "/execute",
		bodyBytes:    bodyBytes,
		sandboxToken: sandboxToken,
		sessionID:    sessionID,
		requestID:    requestID,
	})
}

func (h *CodeInterpreterHandler) DeleteContext(ctx *gin.Context) {
	reqCtx := ctx.Request.Context()
	requestID := observability.RequestIDFromContext(reqCtx)
	ctx.Writer.Header().Set(observability.RequestIDHeader, requestID)

	sessionID := strings.TrimSpace(ctx.GetHeader(sessionHeader))
	if sessionID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	sandboxInfo, err := h.sessionStore.GetSession(reqCtx, sessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			response.ErrorResponse(ctx, response.FormError)
			return
		}
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sessionID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sandboxID", sessionID), zap.Error(err))
	}
	sandboxToken, err := h.issueSandboxToken(sessionID)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	contextID := strings.TrimSpace(ctx.Param("contextId"))
	if contextID == "" {
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	ctx.Writer.Header().Set(sessionHeader, sessionID)
	h.proxySandboxRequest(ctx, proxySandboxRequestOpts{
		target:       target,
		method:       http.MethodDelete,
		path:         "/api/contexts/" + contextID,
		sandboxToken: sandboxToken,
		sessionID:    sessionID,
		requestID:    requestID,
	})
}

func (h *CodeInterpreterHandler) createSandbox(ctx context.Context, language string) (*db.SandboxInfo, error) {
	tracer := otel.Tracer("gateway.codeinterpreter")
	ctx, span := tracer.Start(ctx, "gateway.codeinterpreter.create_rpc")
	defer span.End()

	requestID := observability.RequestIDFromContext(ctx)
	if requestID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, observability.RequestIDHeader, requestID)
		span.SetAttributes(attribute.String("request.id", requestID))
	}
	span.SetAttributes(attribute.String("sandbox.language", language))

	createSandboxResp, err := h.agentCoreServiceClient.CreateCodeInterpreter(ctx, &pb.CreateSandboxRequest{Language: language})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create codeinterpreter rpc failed")
		return nil, fmt.Errorf("create sandbox failed: %w", err)
	}
	span.SetAttributes(attribute.String("agentland.session_id", createSandboxResp.SandboxId))

	return &db.SandboxInfo{
		SandboxID:    createSandboxResp.SandboxId,
		GrpcEndpoint: createSandboxResp.GrpcEndpoint,
	}, nil
}

func (h *CodeInterpreterHandler) issueSandboxToken(sessionID string) (string, error) {
	if h.sandboxTokenSigner == nil {
		return "", errors.New("sandbox token signer is not configured")
	}
	token, err := h.sandboxTokenSigner.Sign(sessionID, "", 0)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (h *CodeInterpreterHandler) proxySandboxRequest(ctx *gin.Context, opts proxySandboxRequestOpts) {
	proxy := httputil.NewSingleHostReverseProxy(opts.target)
	proxy.Transport = h.sandboxTransport

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Method = opts.method
		req.URL.Path = opts.path
		req.Host = opts.target.Host
		req.URL.RawQuery = ctx.Request.URL.RawQuery
		req.Header = ctx.Request.Header.Clone()
		req.Header.Del("Authorization")
		if opts.sandboxToken != "" {
			req.Header.Set("Authorization", "Bearer "+opts.sandboxToken)
		}
		req.Header.Set(observability.RequestIDHeader, opts.requestID)
		if opts.sessionID != "" {
			req.Header.Set(sessionHeader, opts.sessionID)
		}
		otel.GetTextMapPropagator().Inject(req.Context(), propagation.HeaderCarrier(req.Header))

		if opts.bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(opts.bodyBytes))
			req.ContentLength = int64(len(opts.bodyBytes))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(opts.bodyBytes)), nil
			}
			req.Header.Set("Content-Type", "application/json")
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		if opts.sessionID != "" {
			resp.Header.Set(sessionHeader, opts.sessionID)
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		zap.L().Error("Reverse proxy request failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
	}

	proxy.ServeHTTP(closeNotifySafeWriter{ResponseWriter: ctx.Writer}, ctx.Request)
}

func resolveSandboxTarget(endpoint string) (*url.URL, error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return nil, fmt.Errorf("sandbox endpoint is empty")
	}

	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return url.Parse(trimmed)
	}

	return url.Parse("http://" + trimmed)
}

type closeNotifySafeWriter struct {
	gin.ResponseWriter
}

func (w closeNotifySafeWriter) CloseNotify() <-chan bool {
	return nil
}
