package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
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

type AgentSessionHandler struct {
	agentCoreServiceClient pb.AgentCoreServiceClient
	sandboxTransport       http.RoundTripper
	sessionStore           sessionStore
	sandboxTokenSigner     tokenSigner
	defaultRuntimeName     string
	defaultRuntimeNS       string
	harudPort              string
}

func InitAgentSessionApi(group *gin.RouterGroup, cfg *config.Config) {
	h := &AgentSessionHandler{}

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
	h.defaultRuntimeName = cfg.DefaultAgentRuntimeName
	h.defaultRuntimeNS = cfg.DefaultAgentRuntimeNamespace
	h.harudPort = strings.TrimSpace(cfg.HarudPort)
	if h.harudPort == "" {
		h.harudPort = "1885"
	}

	group.POST("/invocations/*path", h.Invoke)
	group.GET("/invocations/*path", h.Invoke)
	group.GET("/:sessionId/fs/tree", h.GetFSTree)
	group.GET("/:sessionId/fs/file", h.GetFSFile)
	group.Any("/:sessionId/endpoints/by-port/:port", h.ProxyByPort)
	group.Any("/:sessionId/endpoints/by-port/:port/*path", h.ProxyByPort)
}

func (h *AgentSessionHandler) Invoke(ctx *gin.Context) {
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		zap.L().Error("Read request body failed", zap.Error(err))
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	sandboxInfo, err := h.resolveSession(ctx)
	if err != nil {
		zap.L().Error("Resolve agent session failed", zap.Error(err))
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

	invokePath := ctx.Param("path")
	if invokePath == "" {
		invokePath = "/"
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = h.sandboxTransport
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Method = ctx.Request.Method
		req.URL.Path = invokePath
		req.URL.RawQuery = ctx.Request.URL.RawQuery
		req.Host = target.Host
		req.Header = ctx.Request.Header.Clone()
		req.Header.Del("Authorization")
		if sandboxToken != "" {
			req.Header.Set("Authorization", "Bearer "+sandboxToken)
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("x-agentland-session", sandboxInfo.SandboxID)
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		zap.L().Error("Reverse proxy invocation failed", zap.Error(err))
		http.Error(w, "sandbox unreachable", http.StatusBadGateway)
	}

	proxy.ServeHTTP(closeNotifySafeWriter{ResponseWriter: ctx.Writer}, ctx.Request)
}

func (h *AgentSessionHandler) GetFSTree(ctx *gin.Context) {
	h.proxySessionRequestToHarud(ctx, "/api/fs/tree")
}

func (h *AgentSessionHandler) GetFSFile(ctx *gin.Context) {
	h.proxySessionRequestToHarud(ctx, "/api/fs/file")
}

func (h *AgentSessionHandler) ProxyByPort(ctx *gin.Context) {
	port := strings.TrimSpace(ctx.Param("port"))
	if port == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "port is required"})
		return
	}

	internalPath := "/api/proxy/by-port/" + port
	if subPath := ctx.Param("path"); subPath != "" {
		internalPath += subPath
	}
	h.proxySessionRequestToHarud(ctx, internalPath)
}

func (h *AgentSessionHandler) proxySessionRequestToHarud(ctx *gin.Context, internalPath string) {
	sessionID := strings.TrimSpace(ctx.Param("sessionId"))
	if sessionID == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "sessionId is required"})
		return
	}

	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		zap.L().Error("Read request body failed", zap.Error(err))
		response.ErrorResponse(ctx, response.FormError)
		return
	}

	sandboxInfo, err := h.sessionStore.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		zap.L().Error("Get session from store failed", zap.String("sessionID", sessionID), zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	if err := h.sessionStore.UpdateLatestActivity(ctx, sessionID); err != nil {
		zap.L().Warn("Update latest activity failed", zap.String("sessionID", sessionID), zap.Error(err))
	}

	sandboxToken, err := h.issueSandboxToken(sessionID)
	if err != nil {
		zap.L().Error("Issue sandbox token failed", zap.String("sessionID", sessionID), zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	target, err := h.resolveHarudTarget(sandboxInfo.GrpcEndpoint)
	if err != nil {
		zap.L().Error("Resolve harud target failed", zap.String("sessionID", sessionID), zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = h.sandboxTransport
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Method = ctx.Request.Method
		req.URL.Path = internalPath
		req.URL.RawQuery = ctx.Request.URL.RawQuery
		req.Host = target.Host
		req.Header = ctx.Request.Header.Clone()
		req.Header.Del("Authorization")
		req.Header.Del("x-agentland-session")
		req.Header.Del("X-Agentland-Session")
		if sandboxToken != "" {
			req.Header.Set("Authorization", "Bearer "+sandboxToken)
		}
		req.Header.Set("x-agentland-session", sessionID)
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Set("x-agentland-session", sessionID)
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		zap.L().Error("Reverse proxy request failed", zap.String("sessionID", sessionID), zap.Error(err))
		http.Error(w, "sandbox unreachable", http.StatusBadGateway)
	}

	proxy.ServeHTTP(closeNotifySafeWriter{ResponseWriter: ctx.Writer}, ctx.Request)
}

func (h *AgentSessionHandler) resolveSession(ctx *gin.Context) (*db.SandboxInfo, error) {
	sessionID := ctx.GetHeader("x-agentland-session")
	if sessionID == "" {
		runtimeName, runtimeNamespace := resolveRuntimeRef(ctx, h.defaultRuntimeName, h.defaultRuntimeNS)
		return h.createAgentSession(ctx, runtimeName, runtimeNamespace)
	}

	sandboxInfo, err := h.sessionStore.GetSession(ctx, sessionID)
	if err == nil {
		return sandboxInfo, nil
	}

	if !errors.Is(err, db.ErrSessionNotFound) {
		return nil, fmt.Errorf("get session info failed: %w", err)
	}

	zap.L().Warn("Session not found, creating new agent session", zap.String("sessionID", sessionID))
	runtimeName, runtimeNamespace := resolveRuntimeRef(ctx, h.defaultRuntimeName, h.defaultRuntimeNS)
	return h.createAgentSession(ctx, runtimeName, runtimeNamespace)
}

func (h *AgentSessionHandler) createAgentSession(ctx context.Context, runtimeName, runtimeNamespace string) (*db.SandboxInfo, error) {
	if strings.TrimSpace(runtimeName) == "" {
		return nil, fmt.Errorf("runtime name is required")
	}

	createResp, err := h.agentCoreServiceClient.CreateAgentSession(ctx, &pb.CreateAgentSessionRequest{
		RuntimeName:      runtimeName,
		RuntimeNamespace: runtimeNamespace,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent session failed: %w", err)
	}

	return &db.SandboxInfo{
		SandboxID:    createResp.SessionId,
		GrpcEndpoint: createResp.GrpcEndpoint,
	}, nil
}

func (h *AgentSessionHandler) issueSandboxToken(sessionID string) (string, error) {
	if h.sandboxTokenSigner == nil {
		return "", errors.New("sandbox token signer is not configured")
	}
	token, err := h.sandboxTokenSigner.Sign(sessionID, "", 0)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (h *AgentSessionHandler) resolveHarudTarget(endpoint string) (*url.URL, error) {
	target, err := resolveSandboxTarget(endpoint)
	if err != nil {
		return nil, err
	}

	host := strings.TrimSpace(target.Hostname())
	if host == "" {
		return nil, fmt.Errorf("sandbox host is empty")
	}

	port := h.harudPort
	if strings.TrimSpace(port) == "" {
		port = "1885"
	}

	target.Scheme = "http"
	target.Host = net.JoinHostPort(host, port)
	return target, nil
}

func resolveRuntimeRef(ctx *gin.Context, defaultRuntimeName, defaultRuntimeNS string) (string, string) {
	runtimeName := strings.TrimSpace(ctx.GetHeader("x-agentland-runtime"))
	if runtimeName == "" {
		runtimeName = strings.TrimSpace(ctx.Query("runtime"))
	}
	if runtimeName == "" {
		runtimeName = strings.TrimSpace(defaultRuntimeName)
	}

	runtimeNamespace := strings.TrimSpace(ctx.GetHeader("x-agentland-runtime-namespace"))
	if runtimeNamespace == "" {
		runtimeNamespace = strings.TrimSpace(ctx.Query("runtime_namespace"))
	}
	if runtimeNamespace == "" {
		runtimeNamespace = strings.TrimSpace(defaultRuntimeNS)
	}

	return runtimeName, runtimeNamespace
}
