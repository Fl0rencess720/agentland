package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const (
	AgentCoreServiceName = "agentcore"
	AgentCoreServicePort = "8082"
)

func resolveAgentCoreAddress() string {
	if addr := os.Getenv("AGENTCORE_ADDRESS"); addr != "" {
		return addr
	}

	return AgentCoreServiceName + ":" + AgentCoreServicePort
}

type CodeInterpreterHandler struct {
	agentCoreServiceClient pb.AgentCoreServiceClient

	sandboxTransport http.RoundTripper
}

type ExecuteCodeReq struct {
	Language string `json:"language" binding:"required"`
	Code     string `json:"code" binding:"required"`
}

type ExecuteCodeResp struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func InitCodeInterpreterApi(group *gin.RouterGroup) {
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

	address := resolveAgentCoreAddress()

	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		zap.L().Error("Connect to service via Kubernetes DNS failed", zap.Error(err))
		return
	}

	h.sandboxTransport = &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

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

	createSandboxResp, err := h.agentCoreServiceClient.CreateSandbox(ctx, &pb.CreateSandboxRequest{
		Language: req.Language,
	})
	if err != nil {
		zap.L().Error("Create Sandbox failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	target, err := resolveSandboxTarget(createSandboxResp.GrpcEndpoint)
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
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		zap.L().Error("Reverse proxy execute failed", zap.Error(err))
		http.Error(w, "sandbox unreachable", http.StatusBadGateway)
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
	type closeNotifier interface {
		CloseNotify() <-chan bool
	}

	type unwrapper interface {
		Unwrap() http.ResponseWriter
	}

	if uw, ok := w.ResponseWriter.(unwrapper); ok {
		if cn, ok := uw.Unwrap().(closeNotifier); ok {
			return cn.CloseNotify()
		}
	}

	ch := make(chan bool, 1)
	return ch
}
