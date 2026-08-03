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
	"github.com/Fl0rencess720/agentland/pkg/common/utils"
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
	SessionHeader              = "x-agentland-session"
	LanguagePython             = "python"
	LanguageBash               = "bash"
	maxGatewayRequestBodyBytes = int64(8 << 20)
	maxPreviewRewriteBytes     = int64(16 << 20)
)

func isSupportedCodeLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case LanguagePython, LanguageBash:
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
	// ResponsePathPrefix rewrites root-relative URLs in browser assets so an
	// application mounted below /p/{token}/ keeps loading from that prefix.
	ResponsePathPrefix string
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
	// Ensure streaming responses (SSE/chunked) are flushed to the client promptly.
	proxy.FlushInterval = 100 * time.Millisecond

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
		if cfg.ResponsePathPrefix != "" {
			// Text responses must be uncompressed before URL rewriting.
			req.Header.Del("Accept-Encoding")
		}

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
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		if cfg.SessionID != "" {
			resp.Header.Set(SessionHeader, cfg.SessionID)
		}
		// Avoid buffering SSE responses in common proxies.
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))), "text/event-stream") {
			resp.Header.Set("Cache-Control", "no-cache")
			resp.Header.Set("X-Accel-Buffering", "no")
		}
		if cfg.ResponsePathPrefix != "" {
			applyPreviewCORS(resp.Header, resp.Request)
			return rewritePrefixedResponse(resp, cfg.ResponsePathPrefix)
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

func applyPreviewCORS(header http.Header, request *http.Request) {
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Content-Type, Accept")
	header.Del("Access-Control-Allow-Credentials")
	if request == nil {
		return
	}
	if requested := strings.TrimSpace(request.Header.Get("Access-Control-Request-Headers")); requested != "" {
		header.Set("Access-Control-Allow-Headers", requested)
	}
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
	return utils.NewSignerFromConfig(utils.SignerConfig{
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
	return readRequestBodyLimit(ctx, maxGatewayRequestBodyBytes)
}

func readRequestBodyLimit(ctx *gin.Context, limit int64) ([]byte, bool) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			ctx.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
			return nil, false
		}
		zap.L().Error("Read request body failed", zap.Error(err))
		response.ErrorResponse(ctx, response.FormError)
		return nil, false
	}
	return bodyBytes, true
}

func rewritePrefixedResponse(resp *http.Response, rawPrefix string) error {
	prefix := "/" + strings.Trim(strings.TrimSpace(rawPrefix), "/") + "/"
	if prefix == "//" {
		return nil
	}
	if location := strings.TrimSpace(resp.Header.Get("Location")); strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "//") && !strings.HasPrefix(location, prefix) {
		resp.Header.Set("Location", strings.TrimSuffix(prefix, "/")+location)
	}
	if resp.Body == nil || !previewTextContent(resp.Header.Get("Content-Type")) || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	original := resp.Body
	data, err := io.ReadAll(io.LimitReader(original, maxPreviewRewriteBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxPreviewRewriteBytes {
		resp.Body = &prefixReadCloser{Reader: io.MultiReader(bytes.NewReader(data), original), Closer: original}
		return nil
	}
	if err := original.Close(); err != nil {
		return err
	}
	rewritten := rewriteRootRelativeURLs(data, prefix)
	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", fmt.Sprint(len(rewritten)))
	return nil
}

func previewTextContent(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return value == "text/html" || value == "text/css" || value == "text/javascript" ||
		value == "application/javascript" || value == "application/x-javascript" || value == "image/svg+xml"
}

func rewriteRootRelativeURLs(data []byte, prefix string) []byte {
	result := make([]byte, 0, len(data)+128)
	for index := 0; index < len(data); index++ {
		current := data[index]
		result = append(result, current)
		if (current == '\'' || current == '"' || current == '`') && index+1 < len(data) && data[index+1] == '/' {
			if index+2 < len(data) && data[index+2] == '/' {
				continue
			}
			if bytes.HasPrefix(data[index+1:], []byte(prefix)) {
				continue
			}
			result = append(result, prefix...)
			index++
			continue
		}
		if current == '(' && index >= 3 && strings.EqualFold(string(data[index-3:index]), "url") && index+1 < len(data) && data[index+1] == '/' {
			if index+2 < len(data) && data[index+2] == '/' {
				continue
			}
			if bytes.HasPrefix(data[index+1:], []byte(prefix)) {
				continue
			}
			result = append(result, prefix...)
			index++
		}
	}
	return result
}

type prefixReadCloser struct {
	io.Reader
	io.Closer
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
