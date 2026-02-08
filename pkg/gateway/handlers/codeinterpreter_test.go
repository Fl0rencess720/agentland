package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type MockAgentCoreServiceClient struct {
	mock.Mock
}

func (m *MockAgentCoreServiceClient) CreateSandbox(ctx context.Context, in *pb.CreateSandboxRequest, opts ...grpc.CallOption) (*pb.CreateSandboxResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pb.CreateSandboxResponse), args.Error(1)
}

type MockSandboxClientManager struct {
	mock.Mock
}

func (m *MockSandboxClientManager) GarbageCollect() {}

func (m *MockSandboxClientManager) Add(sandboxID string, grpcEndpoint string) (pb.SandboxServiceClient, error) {
	args := m.Called(sandboxID, grpcEndpoint)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(pb.SandboxServiceClient), args.Error(1)
}

type MockSandboxServiceClient struct {
	mock.Mock
}

func (m *MockSandboxServiceClient) ExecuteCode(ctx context.Context, in *pb.ExecuteCodeRequest, opts ...grpc.CallOption) (*pb.ExecuteCodeResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pb.ExecuteCodeResponse), args.Error(1)
}

func TestCodeInterpreterSuite(t *testing.T) {
	suite.Run(t, &CodeInterpreterSuite{})
}

type CodeInterpreterSuite struct {
	suite.Suite
	recorder                 *httptest.ResponseRecorder
	ctx                      *gin.Context
	mockAgentCoreClient      *MockAgentCoreServiceClient
	mockSandboxManager       *MockSandboxClientManager
	mockSandboxServiceClient *MockSandboxServiceClient
	handler                  *CodeInterpreterHandler
}

func (s *CodeInterpreterSuite) SetupSuite() {
	gin.SetMode(gin.ReleaseMode)
	zap.ReplaceGlobals(zap.NewNop())
}

func (s *CodeInterpreterSuite) SetupTest() {
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)

	s.mockAgentCoreClient = new(MockAgentCoreServiceClient)
	s.mockSandboxManager = new(MockSandboxClientManager)
	s.mockSandboxServiceClient = new(MockSandboxServiceClient)

	s.handler = &CodeInterpreterHandler{
		agentCoreServiceClient: s.mockAgentCoreClient,
		scm:                    s.mockSandboxManager,
	}
}

func (s *CodeInterpreterSuite) TestExecuteCode_BindError() {
	req := httptest.NewRequest("POST", "/run", bytes.NewBufferString("{bad_json"))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.handler.ExecuteCode(s.ctx)

	s.Equal(400, s.recorder.Code)
	s.mockAgentCoreClient.AssertNotCalled(s.T(), "CreateSandbox")
	s.mockSandboxManager.AssertNotCalled(s.T(), "Add")
}

func (s *CodeInterpreterSuite) TestExecuteCode_GRPCError() {
	reqBody := ExecuteCodeReq{Language: "python", Code: "print('hello')"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/run", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.mockAgentCoreClient.On("CreateSandbox",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "python"},
	).Return(nil, errors.New("rpc connection failed"))

	s.handler.ExecuteCode(s.ctx)

	s.Equal(500, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), "Server Error")
	s.mockSandboxManager.AssertNotCalled(s.T(), "Add")
}

func (s *CodeInterpreterSuite) TestExecuteCode_SandboxClientError() {
	reqBody := ExecuteCodeReq{Language: "go", Code: "fmt.Println(1)"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/run", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.mockAgentCoreClient.On("CreateSandbox",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "go"},
	).Return(&pb.CreateSandboxResponse{
		SandboxId:    "sandbox-uuid-1234",
		GrpcEndpoint: "sandbox:1883",
	}, nil)

	s.mockSandboxManager.On("Add", "sandbox-uuid-1234", "sandbox:1883").Return(nil, errors.New("sandbox client init failed"))

	s.handler.ExecuteCode(s.ctx)

	s.Equal(500, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), "Server Error")
}

func (s *CodeInterpreterSuite) TestExecuteCode_Success() {
	reqBody := ExecuteCodeReq{Language: "go", Code: "fmt.Println(1)"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/run", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.mockAgentCoreClient.On("CreateSandbox",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "go"},
	).Return(&pb.CreateSandboxResponse{
		SandboxId:    "sandbox-uuid-1234",
		GrpcEndpoint: "sandbox:1883",
	}, nil)

	s.mockSandboxManager.On("Add", "sandbox-uuid-1234", "sandbox:1883").Return(s.mockSandboxServiceClient, nil)

	s.mockSandboxServiceClient.On("ExecuteCode",
		mock.Anything,
		&pb.ExecuteCodeRequest{Code: "fmt.Println(1)"},
	).Return(&pb.ExecuteCodeResponse{Stdout: "1\n", Stderr: ""}, nil)

	s.handler.ExecuteCode(s.ctx)

	s.Equal(200, s.recorder.Code)

	var resp map[string]interface{}
	err := json.Unmarshal(s.recorder.Body.Bytes(), &resp)
	s.NoError(err)
	s.Equal(float64(200), resp["code"])
	s.Equal("success", resp["msg"])

	data := resp["data"].(map[string]interface{})
	s.Equal("1\n", data["stdout"])
	s.Equal("", data["stderr"])
}
