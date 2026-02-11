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

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/Fl0rencess720/agentland/pkg/common/sandboxtoken"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
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
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		zap.L().Error("Read request body failed", zap.Error(err))
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	var req ExecuteCodeReq
	if err := json.Unmarshal(bodyBytes, &req); err != nil || req.Language == "" || req.Code == "" {
		zap.L().Error("Bind request failed", zap.Error(err))
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	sandboxInfo, err := h.resolveSandbox(ctx, req.Language)
	if err != nil {
		zap.L().Error("Resolve sandbox failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	if err := h.sessionStore.UpdateLatestActivity(ctx, sandboxInfo.SandboxID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sandboxID", sandboxInfo.SandboxID), zap.Error(err))
	}

	sandboxToken, err := h.issueSandboxToken(sandboxInfo.SandboxID)
	if err != nil {
		zap.L().Error("Issue sandbox token failed", zap.String("sandboxID", sandboxInfo.SandboxID), zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	target, err := resolveSandboxTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		zap.L().Error("Parse sandbox url failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = h.sandboxTransport
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
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("x-agentland-session", sandboxInfo.SandboxID)
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		zap.L().Error("Reverse proxy execute failed", zap.Error(err))
		http.Error(w, "sandbox unreachable", http.StatusBadGateway)
	}

	proxy.ServeHTTP(closeNotifySafeWriter{ResponseWriter: ctx.Writer}, ctx.Request)
}

func (h *CodeInterpreterHandler) resolveSandbox(ctx *gin.Context, language string) (*db.SandboxInfo, error) {
	sessionID := ctx.GetHeader("x-agentland-session")
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
	createSandboxResp, err := h.agentCoreServiceClient.CreateSandbox(ctx, &pb.CreateSandboxRequest{Language: language})
	if err != nil {
		return nil, fmt.Errorf("create sandbox failed: %w", err)
	}

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
