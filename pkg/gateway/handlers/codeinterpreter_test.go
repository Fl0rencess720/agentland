package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCodeInterpreterSuite(t *testing.T) {
	suite.Run(t, &CodeInterpreterSuite{})
}

type CodeInterpreterSuite struct {
	suite.Suite
	recorder            *httptest.ResponseRecorder
	ctx                 *gin.Context
	mockAgentCoreClient *MockAgentCoreServiceClient
	handler             *CodeInterpreterHandler
}

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (s *CodeInterpreterSuite) SetupSuite() {
	gin.SetMode(gin.ReleaseMode)
	zap.ReplaceGlobals(zap.NewNop())
}

func (s *CodeInterpreterSuite) SetupTest() {
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)

	s.mockAgentCoreClient = new(MockAgentCoreServiceClient)

	s.handler = &CodeInterpreterHandler{
		agentCoreServiceClient: s.mockAgentCoreClient,
		sandboxTransport:       http.DefaultTransport,
	}
}

func (s *CodeInterpreterSuite) TestExecuteCode_BindError() {
	req := httptest.NewRequest("POST", "/run", bytes.NewBufferString("{bad_json"))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.handler.ExecuteCode(s.ctx)

	s.Equal(400, s.recorder.Code)
	s.mockAgentCoreClient.AssertNotCalled(s.T(), "CreateSandbox")
}

func (s *CodeInterpreterSuite) TestExecuteCode_CreateSandboxError() {
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
}

func (s *CodeInterpreterSuite) TestExecuteCode_EmptyEndpointError() {
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
		GrpcEndpoint: "",
	}, nil)

	s.handler.ExecuteCode(s.ctx)

	s.Equal(500, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), "Server Error")
}

func (s *CodeInterpreterSuite) TestExecuteCode_ProxySuccess() {
	reqBody := ExecuteCodeReq{Language: "go", Code: "fmt.Println(1)"}
	jsonBytes, _ := json.Marshal(reqBody)

	s.handler.sandboxTransport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/api/execute", r.URL.Path)
		s.Equal("application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		s.NoError(err)
		s.JSONEq(string(jsonBytes), string(body))

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"exit_code":0,"stdout":"1\n","stderr":""}`)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest("POST", "/run", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.mockAgentCoreClient.On("CreateSandbox",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "go"},
	).Return(&pb.CreateSandboxResponse{
		SandboxId:    "sandbox-uuid-1234",
		GrpcEndpoint: "sandbox.test:1883",
	}, nil)

	s.handler.ExecuteCode(s.ctx)

	s.Equal(200, s.recorder.Code)
	s.JSONEq(`{"exit_code":0,"stdout":"1\n","stderr":""}`, s.recorder.Body.String())
}

func (s *CodeInterpreterSuite) TestExecuteCode_ProxyUnreachable() {
	reqBody := ExecuteCodeReq{Language: "go", Code: "fmt.Println(1)"}
	jsonBytes, _ := json.Marshal(reqBody)

	s.handler.sandboxTransport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})

	req := httptest.NewRequest("POST", "/run", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.mockAgentCoreClient.On("CreateSandbox",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "go"},
	).Return(&pb.CreateSandboxResponse{
		SandboxId:    "sandbox-uuid-1234",
		GrpcEndpoint: "sandbox.test:1883",
	}, nil)

	s.handler.ExecuteCode(s.ctx)

	s.Equal(502, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), "sandbox unreachable")
}
