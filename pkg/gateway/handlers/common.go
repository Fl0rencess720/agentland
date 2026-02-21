package handlers

import (
	"bytes"
	"context"
	"encoding/json"
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
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const (
	SessionHeader  = "x-agentland-session"
	LanguagePython = "python"
	LanguageShell  = "shell"
)

func isSupportedCodeLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case LanguagePython, LanguageShell:
		return true
	default:
		return false
	}
}

type SessionStore interface {
	GetSession(ctx context.Context, sandboxID string) (*db.SandboxInfo, error)
	UpdateLatestActivity(ctx context.Context, sandboxID string) error
}

type TokenSigner interface {
	Sign(sessionID, subject string, version int64) (string, error)
}

type ProxyEngine struct {
	Transport http.RoundTripper
}

type ProxyConfig struct {
	Target       *url.URL
	Method       string
	InternalPath string
	Body         []byte
	SessionID    string
	SandboxToken string
	RequestID    string
}

func NewProxyEngine() *ProxyEngine {
	return &ProxyEngine{
		Transport: &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// Forward 执行 HTTP 代理、Header 注入及 Body 恢复
func (e *ProxyEngine) Forward(ctx *gin.Context, cfg ProxyConfig) {
	proxy := httputil.NewSingleHostReverseProxy(cfg.Target)
	proxy.Transport = e.Transport

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Method = cfg.Method
		req.URL.Path = cfg.InternalPath
		req.Host = cfg.Target.Host
		req.URL.RawQuery = ctx.Request.URL.RawQuery

		req.Header = ctx.Request.Header.Clone()
		req.Header.Del("Authorization")
		req.Header.Del(SessionHeader)
		req.Header.Del("X-Agentland-Session")

		if cfg.SandboxToken != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.SandboxToken)
		}
		if cfg.SessionID != "" {
			req.Header.Set(SessionHeader, cfg.SessionID)
		}
		if cfg.RequestID != "" {
			req.Header.Set(observability.RequestIDHeader, cfg.RequestID)
		}

		// 注入 OpenTelemetry 链路追踪
		otel.GetTextMapPropagator().Inject(req.Context(), propagation.HeaderCarrier(req.Header))

		// 恢复 Body
		if cfg.Body != nil {
			req.Body = io.NopCloser(bytes.NewReader(cfg.Body))
			req.ContentLength = int64(len(cfg.Body))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(cfg.Body)), nil
			}
			req.Header.Set("Content-Type", "application/json")
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		if cfg.SessionID != "" {
			resp.Header.Set(SessionHeader, cfg.SessionID)
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		zap.L().Error(
			"Reverse proxy request failed",
			zap.String("target", cfg.Target.String()),
			zap.String("session_id", cfg.SessionID),
			zap.String("request_id", cfg.RequestID),
			zap.Error(err),
		)
		http.Error(w, "sandbox unreachable", http.StatusBadGateway)
	}

	proxy.ServeHTTP(closeNotifySafeWriter{ResponseWriter: ctx.Writer}, ctx.Request)
}

func BuildAgentCoreClient(address string) (pb.AgentCoreServiceClient, error) {
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
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, err
	}
	return pb.NewAgentCoreServiceClient(conn), nil
}

func BuildTokenSigner(cfg *config.Config) (TokenSigner, error) {
	return sandboxtoken.NewSignerFromConfig(sandboxtoken.SignerConfig{
		PrivateKeyPath: cfg.SandboxJWTPrivatePath,
		Issuer:         cfg.SandboxJWTIssuer,
		Audience:       cfg.SandboxJWTAudience,
		KID:            cfg.SandboxJWTKID,
		TTL:            cfg.SandboxJWTTTL,
	})
}

func resolveSandboxTarget(endpoint string) (*url.URL, error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return nil, fmt.Errorf("sandbox endpoint is empty")
	}
	if !strings.HasPrefix(trimmed, "http") {
		trimmed = "http://" + trimmed
	}
	return url.Parse(trimmed)
}

func initRequestContext(ctx *gin.Context) (context.Context, string) {
	reqCtx := ctx.Request.Context()
	requestID := observability.RequestIDFromContext(reqCtx)
	ctx.Writer.Header().Set(observability.RequestIDHeader, requestID)
	return reqCtx, requestID
}

func readRequestBody(ctx *gin.Context) ([]byte, bool) {
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		zap.L().Error("Read request body failed", zap.Error(err))
		response.ErrorResponse(ctx, response.FormError)
		return nil, false
	}
	return bodyBytes, true
}

func bindJSONWithBody(ctx *gin.Context, obj interface{}) ([]byte, bool) {
	bodyBytes, ok := readRequestBody(ctx)
	if !ok {
		return nil, false
	}
	ctx.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	if err := json.Unmarshal(bodyBytes, obj); err != nil {
		response.ErrorResponse(ctx, response.FormError)
		return nil, false
	}
	return bodyBytes, true
}

type closeNotifySafeWriter struct {
	gin.ResponseWriter
}

func (w closeNotifySafeWriter) CloseNotify() <-chan bool {
	return nil
}
