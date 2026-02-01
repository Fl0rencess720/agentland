package handlers

import (
	"time"

	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	pb "github.com/Fl0rencess720/agentland/rpc"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const (
	AgentCoreServiceName = "localhost"
	AgentCoreServicePort = "8082"
)

type CodeRunnerHandler struct {
	agentCoreServiceClient pb.AgentCoreServiceClient
	scm                    *SandboxClientManager
}

type ExecuteCodeReq struct {
	Language string `json:"language"`
	Code     string `json:"code"`
}

type ExecuteCodeResp struct {
	Result string `json:"result"`
}

func InitCodeRunnerApi(group *gin.RouterGroup) {
	h := &CodeRunnerHandler{}

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

	address := AgentCoreServiceName + ":" + AgentCoreServicePort

	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		zap.L().Error("Connect to service via Kubernetes DNS failed", zap.Error(err))
		return
	}

	scm := NewSandboxClientManager()

	h.agentCoreServiceClient = pb.NewAgentCoreServiceClient(conn)
	h.scm = scm

	group.POST("/run", h.ExecuteCode)
}

func (h *CodeRunnerHandler) ExecuteCode(ctx *gin.Context) {
	var req ExecuteCodeReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
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

	grpcEndpoint := createSandboxResp.GrpcEndpoint
	client, err := h.scm.Add(createSandboxResp.SandboxId, grpcEndpoint)
	if err != nil {
		zap.L().Error("Add Sandbox client failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	excuteCodeResp, err := client.ExecuteCode(ctx, &pb.ExecuteCodeRequest{
		Code: req.Code,
	})
	if err != nil {
		zap.L().Error("Execute code failed", zap.Error(err))
		response.ErrorResponse(ctx, response.ServerError)
		return
	}

	response.SuccessResponse(ctx, ExecuteCodeResp{
		Result: excuteCodeResp.Stdout,
	})
}
