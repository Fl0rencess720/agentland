package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/common/testutil"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type MockAgentCoreServiceClient struct {
	mock.Mock
}

type mockSessionStore struct {
	getSessionFn           func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error)
	updateLatestActivityFn func(ctx context.Context, sandboxID string) error
}

type mockTokenSigner struct {
	signFn func(sessionID, subject string, version int64) (string, error)
}

func (m *mockSessionStore) GetSession(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
	if m.getSessionFn != nil {
		return m.getSessionFn(ctx, sandboxID)
	}
	return nil, db.ErrSessionNotFound
}

func (m *mockSessionStore) UpdateLatestActivity(ctx context.Context, sandboxID string) error {
	if m.updateLatestActivityFn != nil {
		return m.updateLatestActivityFn(ctx, sandboxID)
	}
	return nil
}

func (m *mockTokenSigner) Sign(sessionID, subject string, version int64) (string, error) {
	if m.signFn != nil {
		return m.signFn(sessionID, subject, version)
	}
	return "", errors.New("sign not implemented")
}

func (m *MockAgentCoreServiceClient) CreateCodeInterpreter(ctx context.Context, in *pb.CreateSandboxRequest, opts ...grpc.CallOption) (*pb.CreateSandboxResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pb.CreateSandboxResponse), args.Error(1)
}

func (m *MockAgentCoreServiceClient) CreateAgentSession(ctx context.Context, in *pb.CreateAgentSessionRequest, opts ...grpc.CallOption) (*pb.CreateAgentSessionResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pb.CreateAgentSessionResponse), args.Error(1)
}

func (m *MockAgentCoreServiceClient) GetAgentSession(ctx context.Context, in *pb.GetAgentSessionRequest, opts ...grpc.CallOption) (*pb.GetAgentSessionResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pb.GetAgentSessionResponse), args.Error(1)
}

func (m *MockAgentCoreServiceClient) DeleteAgentSession(ctx context.Context, in *pb.DeleteAgentSessionRequest, opts ...grpc.CallOption) (*pb.DeleteAgentSessionResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pb.DeleteAgentSessionResponse), args.Error(1)
}

func TestCodeInterpreterSuite(t *testing.T) {
	suite.Run(t, &CodeInterpreterSuite{})
}

func TestInitCodeInterpreterApi_RegistersSessionFSRoutes(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	privatePath, _, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)

	prevAddress := viper.GetString("agentcore.address")
	t.Cleanup(func() {
		viper.Set("agentcore.address", prevAddress)
	})
	viper.Set("agentcore.address", "dns:///127.0.0.1:18082")

	cfg := &config.Config{
		SandboxJWTPrivatePath:        privatePath,
		SandboxJWTIssuer:             "agentland-gateway",
		SandboxJWTAudience:           "sandbox",
		SandboxJWTTTL:                5 * time.Minute,
		SandboxJWTKID:                "default",
		DefaultAgentRuntimeName:      "default-runtime",
		DefaultAgentRuntimeNamespace: "agentland-sandboxes",
	}

	r := gin.New()
	api := r.Group("/api")
	InitCodeInterpreterApi(api.Group("/code-runner"), cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/code-runner/fs/tree?path=.", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusNotFound, rec.Code)
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
		agentCoreClient: s.mockAgentCoreClient,
		proxyEngine:     &ProxyEngine{Transport: http.DefaultTransport},
		sessionStore:    &mockSessionStore{},
		tokenSigner: &mockTokenSigner{
			signFn: func(sessionID, subject string, version int64) (string, error) {
				return "default.jwt.token", nil
			},
		},
	}
}

func (s *CodeInterpreterSuite) TestCreateContext_ProxySuccess() {
	reqBody := CreateContextReq{Language: "python", CWD: "/workspace"}
	jsonBytes, _ := json.Marshal(reqBody)

	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			s.Equal("session-1", sandboxID)
			return &db.SandboxInfo{
				SandboxID:    "session-1",
				GrpcEndpoint: "sandbox.test:1883",
			}, nil
		},
	}

	s.handler.proxyEngine.Transport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/api/contexts", r.URL.Path)
		s.Equal("Bearer default.jwt.token", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		s.NoError(err)
		s.JSONEq(string(jsonBytes), string(body))
		resp := &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"context_id":"ctx-1","language":"python","cwd":"/workspace","state":"ready","created_at":"2026-02-17T08:30:00Z"}`,
			)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest("POST", "/contexts", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-agentland-session", "session-1")
	s.ctx.Request = req

	s.handler.CreateContext(s.ctx)

	s.Equal(http.StatusCreated, s.recorder.Code)
	s.Equal("session-1", s.recorder.Header().Get("x-agentland-session"))
	s.Contains(s.recorder.Body.String(), `"context_id":"ctx-1"`)
}

func (s *CodeInterpreterSuite) TestCreateSandbox_Success() {
	reqBody := CreateSandboxReq{Language: "python"}
	jsonBytes, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/sandboxes", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.mockAgentCoreClient.On("CreateCodeInterpreter",
		mock.Anything,
		&pb.CreateSandboxRequest{Language: "python"},
	).Return(&pb.CreateSandboxResponse{
		SandboxId:    "session-sbx-1",
		GrpcEndpoint: "sandbox.test:1883",
	}, nil).Once()

	s.handler.CreateSandbox(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"sandbox_id":"session-sbx-1"`)
}

func (s *CodeInterpreterSuite) TestCreateContext_MissingSession() {
	reqBody := CreateContextReq{Language: "python", CWD: "/workspace"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/contexts", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.handler.CreateContext(s.ctx)

	s.Equal(http.StatusBadRequest, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"msg":"Form Error"`)
}

func (s *CodeInterpreterSuite) TestExecuteInContext_MissingSession() {
	reqBody := ExecuteInContextReq{Code: "print(1)"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/contexts/ctx-1/execute", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "contextId", Value: "ctx-1"}}

	s.handler.ExecuteInContext(s.ctx)

	s.Equal(http.StatusBadRequest, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"msg":"Form Error"`)
}

func (s *CodeInterpreterSuite) TestExecuteInContext_ProxySuccess() {
	reqBody := ExecuteInContextReq{Code: "print(1)", TimeoutMs: 30000}
	jsonBytes, _ := json.Marshal(reqBody)

	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			s.Equal("session-1", sandboxID)
			return &db.SandboxInfo{
				SandboxID:    "session-1",
				GrpcEndpoint: "sandbox.test:1883",
			}, nil
		},
	}

	s.handler.proxyEngine.Transport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/api/contexts/ctx-1/execute", r.URL.Path)
		s.Equal("Bearer default.jwt.token", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		s.NoError(err)
		s.JSONEq(string(jsonBytes), string(body))
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"context_id":"ctx-1","execution_count":1,"exit_code":0,"stdout":"1\n","stderr":"","duration_ms":5}`,
			)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest("POST", "/contexts/ctx-1/execute", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-agentland-session", "session-1")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "contextId", Value: "ctx-1"}}

	s.handler.ExecuteInContext(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("session-1", s.recorder.Header().Get("x-agentland-session"))
	s.Contains(s.recorder.Body.String(), `"context_id":"ctx-1"`)
}

func (s *CodeInterpreterSuite) TestGetFSTree_ProxySuccess() {
	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			s.Equal("session-1", sandboxID)
			return &db.SandboxInfo{SandboxID: "session-1", GrpcEndpoint: "sandbox.test:1883"}, nil
		},
	}

	s.handler.proxyEngine.Transport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodGet, r.Method)
		s.Equal("sandbox.test:1883", r.URL.Host)
		s.Equal("/api/fs/tree", r.URL.Path)
		s.Equal("path=src&depth=2", r.URL.RawQuery)
		s.Equal("Bearer default.jwt.token", r.Header.Get("Authorization"))
		s.Equal("session-1", r.Header.Get("x-agentland-session"))

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"root":"src","nodes":[]}`)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/fs/tree?path=src&depth=2", nil)
	req.Header.Set("x-agentland-session", "session-1")
	s.ctx.Request = req

	s.handler.GetFSTree(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("session-1", s.recorder.Header().Get("x-agentland-session"))
	s.JSONEq(`{"root":"src","nodes":[]}`, s.recorder.Body.String())
}

func (s *CodeInterpreterSuite) TestGetFSTree_SessionNotFound() {
	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			return nil, db.ErrSessionNotFound
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/fs/tree", nil)
	req.Header.Set("x-agentland-session", "missing")
	s.ctx.Request = req

	s.handler.GetFSTree(s.ctx)

	s.Equal(http.StatusNotFound, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), "session not found")
}

func (s *CodeInterpreterSuite) TestWriteFSFile_ProxySuccess() {
	reqBody := WriteFSFileReq{
		Path:     "/home/user/data.txt",
		Content:  "这是测试数据\n第二行数据",
		Encoding: "utf-8",
	}
	jsonBytes, _ := json.Marshal(reqBody)

	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			s.Equal("session-1", sandboxID)
			return &db.SandboxInfo{SandboxID: "session-1", GrpcEndpoint: "sandbox.test:1883"}, nil
		},
	}

	s.handler.proxyEngine.Transport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/api/fs/file", r.URL.Path)
		s.Equal("Bearer default.jwt.token", r.Header.Get("Authorization"))
		body, err := io.ReadAll(r.Body)
		s.NoError(err)
		s.JSONEq(string(jsonBytes), string(body))
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"path":"/home/user/data.txt","size":26,"encoding":"utf8"}`)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/fs/file", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-agentland-session", "session-1")
	s.ctx.Request = req

	s.handler.WriteFSFile(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("session-1", s.recorder.Header().Get("x-agentland-session"))
	s.Contains(s.recorder.Body.String(), `"/home/user/data.txt"`)
}

func (s *CodeInterpreterSuite) TestUploadFSFile_ProxySuccess() {
	var reqBody bytes.Buffer
	writer := multipart.NewWriter(&reqBody)
	part, err := writer.CreateFormFile("file", "dataset.csv")
	s.NoError(err)
	_, err = part.Write([]byte("name,value\nalice,1\n"))
	s.NoError(err)
	s.NoError(writer.WriteField("target_file_path", "/workspace/dataset.csv"))
	s.NoError(writer.Close())

	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			s.Equal("session-1", sandboxID)
			return &db.SandboxInfo{SandboxID: "session-1", GrpcEndpoint: "sandbox.test:1883"}, nil
		},
	}

	s.handler.proxyEngine.Transport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/api/fs/upload", r.URL.Path)
		s.True(strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data"))
		s.Equal("Bearer default.jwt.token", r.Header.Get("Authorization"))
		body, readErr := io.ReadAll(r.Body)
		s.NoError(readErr)
		s.Contains(string(body), "name=\"target_file_path\"")
		s.Contains(string(body), "/workspace/dataset.csv")
		s.Contains(string(body), "name,value")
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":200,"msg":"success","data":{"source_path":"dataset.csv","target_path":"/workspace/dataset.csv","size":123}}`)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/fs/upload", &reqBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("x-agentland-session", "session-1")
	s.ctx.Request = req

	s.handler.UploadFSFile(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("session-1", s.recorder.Header().Get("x-agentland-session"))
	s.Contains(s.recorder.Body.String(), `"/workspace/dataset.csv"`)
}

func (s *CodeInterpreterSuite) TestDownloadFSFile_ProxySuccess() {
	path := "/workspace/result.csv"
	queryPath := url.QueryEscape(path)

	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			s.Equal("session-1", sandboxID)
			return &db.SandboxInfo{SandboxID: "session-1", GrpcEndpoint: "sandbox.test:1883"}, nil
		},
	}

	s.handler.proxyEngine.Transport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodGet, r.Method)
		s.Equal("/api/fs/download", r.URL.Path)
		s.Equal("path="+queryPath, r.URL.RawQuery)
		s.Equal("Bearer default.jwt.token", r.Header.Get("Authorization"))
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("id,score\n1,100\n")),
		}
		resp.Header.Set("Content-Type", "application/octet-stream")
		resp.Header.Set("Content-Disposition", "attachment; filename=\"result.csv\"")
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/fs/download?path="+queryPath, nil)
	req.Header.Set("x-agentland-session", "session-1")
	s.ctx.Request = req

	s.handler.DownloadFSFile(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("session-1", s.recorder.Header().Get("x-agentland-session"))
	s.Equal("id,score\n1,100\n", s.recorder.Body.String())
	s.Contains(s.recorder.Header().Get("Content-Disposition"), "result.csv")
}
