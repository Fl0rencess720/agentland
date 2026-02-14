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

type ExecuteCodeReq struct {
	Language string `json:"language" binding:"required"`
	Code     string `json:"code" binding:"required"`
}

type ExecuteCodeResp struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

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

	group.POST("/run", h.ExecuteCode)
}

func (h *CodeInterpreterHandler) ExecuteCode(ctx *gin.Context) {
	tracer := otel.Tracer("gateway.codeinterpreter")
	reqCtx, reqSpan := tracer.Start(ctx.Request.Context(), "gateway.codeinterpreter.request")
	defer reqSpan.End()

	requestID := observability.RequestIDFromContext(reqCtx)
	reqCtx = observability.ContextWithRequestID(reqCtx, requestID)
	ctx.Request = ctx.Request.WithContext(reqCtx)
	ctx.Writer.Header().Set(observability.RequestIDHeader, requestID)
	reqSpan.SetAttributes(attribute.String("request.id", requestID))

	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		zap.L().Error("Read request body failed", zap.Error(err))
		reqSpan.RecordError(err)
		reqSpan.SetStatus(codes.Error, "read request body failed")
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	var req ExecuteCodeReq
	if err := json.Unmarshal(bodyBytes, &req); err != nil || req.Language == "" || req.Code == "" {
		zap.L().Error("Bind request failed", zap.Error(err))
		if err != nil {
			reqSpan.RecordError(err)
		}
		reqSpan.SetStatus(codes.Error, "invalid request payload")
		response.ErrorResponse(ctx, response.FormError)
		return
	}
	reqSpan.SetAttributes(attribute.String("sandbox.language", req.Language))

	resolveCtx, resolveSpan := tracer.Start(reqCtx, "gateway.codeinterpreter.resolve_session")
	ctx.Request = ctx.Request.WithContext(resolveCtx)
	sandboxInfo, err := h.resolveSandbox(resolveCtx, ctx.GetHeader("x-agentland-session"), req.Language)
	resolveSpan.End()
	if err != nil {
		zap.L().Error("Resolve sandbox failed", zap.Error(err))
		reqSpan.RecordError(err)
		reqSpan.SetStatus(codes.Error, "resolve sandbox failed")
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	reqSpan.SetAttributes(attribute.String("agentland.session_id", sandboxInfo.SandboxID))

	if err := h.sessionStore.UpdateLatestActivity(reqCtx, sandboxInfo.SandboxID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sandboxID", sandboxInfo.SandboxID), zap.Error(err))
	}

	sandboxToken, err := h.issueSandboxToken(sandboxInfo.SandboxID)
	if err != nil {
		zap.L().Error("Issue sandbox token failed", zap.String("sandboxID", sandboxInfo.SandboxID), zap.Error(err))
		reqSpan.RecordError(err)
		reqSpan.SetStatus(codes.Error, "issue sandbox token failed")
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		zap.L().Error("Parse sandbox url failed", zap.Error(err))
		reqSpan.RecordError(err)
		reqSpan.SetStatus(codes.Error, "invalid sandbox endpoint")
		response.ErrorResponse(ctx, response.ServerError)
		return
	}
	reqSpan.SetAttributes(attribute.String("sandbox.endpoint", target.String()))

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = h.sandboxTransport
	proxyCtx, proxySpan := tracer.Start(reqCtx, "gateway.codeinterpreter.proxy_execute")
	defer proxySpan.End()

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Method = http.MethodPost
		req.URL.Path = "/api/execute"
		req.Host = target.Host
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Del("Authorization")
		if sandboxToken != "" {
			req.Header.Set("Authorization", "Bearer "+sandboxToken)
		}
		req.Header.Set(observability.RequestIDHeader, requestID)
		otel.GetTextMapPropagator().Inject(req.Context(), propagation.HeaderCarrier(req.Header))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("x-agentland-session", sandboxInfo.SandboxID)
		proxySpan.SetAttributes(attribute.Int("sandbox.execute.status_code", resp.StatusCode))
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		zap.L().Error("Reverse proxy execute failed", zap.Error(err))
		proxySpan.RecordError(err)
		proxySpan.SetStatus(codes.Error, "sandbox proxy failed")
		http.Error(w, "sandbox unreachable", http.StatusBadGateway)
	}

	ctx.Request = ctx.Request.WithContext(proxyCtx)
	proxy.ServeHTTP(closeNotifySafeWriter{ResponseWriter: ctx.Writer}, ctx.Request)
}

func (h *CodeInterpreterHandler) resolveSandbox(ctx context.Context, sessionID, language string) (*db.SandboxInfo, error) {
	if sessionID == "" {
		return h.createSandbox(ctx, language)
	}

	sandboxInfo, err := h.sessionStore.GetSession(ctx, sessionID)
	if err == nil {
		return sandboxInfo, nil
	}

	if !errors.Is(err, db.ErrSessionNotFound) {
		return nil, fmt.Errorf("get sandbox info failed: %w", err)
	}

	zap.L().Warn("Session not found, creating new sandbox", zap.String("sessionID", sessionID))
	return h.createSandbox(ctx, language)
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
