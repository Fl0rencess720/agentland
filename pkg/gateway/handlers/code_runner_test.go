package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	pb "github.com/Fl0rencess720/agentland/rpc"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type MockAgentCoreServiceClient struct {
	mock.Mock
}

// 实现接口方法 CreateSandbox
func (m *MockAgentCoreServiceClient) CreateSandbox(ctx context.Context, in *pb.CreateSandboxRequest, opts ...grpc.CallOption) (*pb.CreateSandboxResponse, error) {
	args := m.Called(ctx, in)

	// 获取 mock 设置的返回值
	// Get(0) 是第一个返回值 (*pb.CreateSandboxResponse)
	// Get(1) 是第二个返回值 (error)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pb.CreateSandboxResponse), args.Error(1)
}

// 当前仅测试能否正常连通 AgentCore
func TestCodeRunnerSuite(t *testing.T) {
	suite.Run(t, &CodeRunnerSuite{})
}

type CodeRunnerSuite struct {
	suite.Suite
	recorder   *httptest.ResponseRecorder
	ctx        *gin.Context
	mockClient *MockAgentCoreServiceClient
	handler    *CodeRunnerHandler
}

func (s *CodeRunnerSuite) SetupSuite() {
	gin.SetMode(gin.ReleaseMode)
	zap.ReplaceGlobals(zap.NewNop())
}

func (s *CodeRunnerSuite) SetupTest() {
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)

	// 初始化 Mock
	s.mockClient = new(MockAgentCoreServiceClient)

	// 初始化被测 Handler
	s.handler = &CodeRunnerHandler{
		agentCoreServiceClient: s.mockClient,
	}
}

// 请求参数绑定失败 (Bad Request)
func (s *CodeRunnerSuite) TestExecuteCode_BindError() {
	// 构造错误的 Body (JSON 格式不对)
	req := httptest.NewRequest("POST", "/run", bytes.NewBufferString("{bad_json"))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	// 调用 Handler
	s.handler.ExecuteCode(s.ctx)

	// 断言
	s.Equal(400, s.recorder.Code)
	// 确保 mock 没有被调用
	s.mockClient.AssertNotCalled(s.T(), "CreateSandbox")
}

// gRPC 调用失败
func (s *CodeRunnerSuite) TestExecuteCode_GRPCError() {
	// 构造正常的 Request
	reqBody := ExecuteCodeReq{Language: "python", Code: "print('hello')"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/run", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	// 设定 Mock 行为
	// 当调用 CreateSandbox，且参数包含 Language="python" 时返回 nil 和 一个错误
	s.mockClient.On("CreateSandbox",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "python"},
	).Return(nil, errors.New("rpc connection failed"))

	// 调用 Handler
	s.handler.ExecuteCode(s.ctx)

	// 断言
	s.Equal(500, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), "Server Error")
}

// 成功情况
func (s *CodeRunnerSuite) TestExecuteCode_Success() {
	// 构造正常的 Request
	reqBody := ExecuteCodeReq{Language: "go", Code: "fmt.Println(1)"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/run", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	// 设定 Mock 行为
	// 当前期望返回成功的 SandboxId
	expectedResp := &pb.CreateSandboxResponse{SandboxId: "sandbox-uuid-1234"}
	s.mockClient.On("CreateSandbox",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "go"},
	).Return(expectedResp, nil)

	// 调用 Handler
	s.handler.ExecuteCode(s.ctx)

	s.Equal(200, s.recorder.Code)

	// 解析响应验证 SandboxId
	var resp map[string]interface{}
	json.Unmarshal(s.recorder.Body.Bytes(), &resp)

	data := resp["data"].(map[string]interface{})
	s.Equal("sandbox-uuid-1234", data["result"])
}
