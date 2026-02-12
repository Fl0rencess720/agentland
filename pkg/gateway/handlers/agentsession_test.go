package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

func TestAgentSessionHandlerSuite(t *testing.T) {
	suite.Run(t, &AgentSessionHandlerSuite{})
}

type AgentSessionHandlerSuite struct {
	suite.Suite
	recorder            *httptest.ResponseRecorder
	ctx                 *gin.Context
	mockAgentCoreClient *MockAgentCoreServiceClient
	handler             *AgentSessionHandler
}

func (s *AgentSessionHandlerSuite) SetupSuite() {
	gin.SetMode(gin.ReleaseMode)
	zap.ReplaceGlobals(zap.NewNop())
}

func (s *AgentSessionHandlerSuite) SetupTest() {
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)

	s.mockAgentCoreClient = new(MockAgentCoreServiceClient)

	s.handler = &AgentSessionHandler{
		agentCoreServiceClient: s.mockAgentCoreClient,
		sandboxTransport:       http.DefaultTransport,
		sessionStore:           &mockSessionStore{},
		defaultRuntimeName:     "default-runtime",
		defaultRuntimeNS:       "agentland-sandboxes",
		sandboxTokenSigner: &mockTokenSigner{
			signFn: func(sessionID, subject string, version int64) (string, error) {
				return "agent.jwt.token", nil
			},
		},
	}
}

func (s *AgentSessionHandlerSuite) TestInvoke_CreateSessionAndProxy() {
	payload := map[string]any{"prompt": "hello"}
	jsonBytes, _ := json.Marshal(payload)

	s.handler.sandboxTransport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/chat", r.URL.Path)
		s.Equal("trace=1", r.URL.RawQuery)
		s.Equal("Bearer agent.jwt.token", r.Header.Get("Authorization"))

		body, err := io.ReadAll(r.Body)
		s.NoError(err)
		s.JSONEq(string(jsonBytes), string(body))

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest("POST", "/invocations/chat?trace=1", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "path", Value: "/chat"}}

	s.mockAgentCoreClient.On("CreateAgentSession",
		mock.Anything,
		&pb.CreateAgentSessionRequest{
			RuntimeName:      "default-runtime",
			RuntimeNamespace: "agentland-sandboxes",
		},
	).Return(&pb.CreateAgentSessionResponse{
		SessionId:    "agent-session-123",
		GrpcEndpoint: "sandbox.test:1883",
	}, nil).Once()

	s.handler.Invoke(s.ctx)

	s.Equal(200, s.recorder.Code)
	s.Equal("agent-session-123", s.recorder.Header().Get("x-agentland-session"))
	s.JSONEq(`{"ok":true}`, s.recorder.Body.String())
	s.mockAgentCoreClient.AssertExpectations(s.T())
}

func (s *AgentSessionHandlerSuite) TestInvoke_ReuseSessionFromHeader() {
	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			s.Equal("existing-session", sandboxID)
			return &db.SandboxInfo{SandboxID: "existing-session", GrpcEndpoint: "sandbox.test:1883"}, nil
		},
	}

	s.handler.sandboxTransport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}
		resp.Header.Set("Content-Type", "application/json")
		return resp, nil
	})

	req := httptest.NewRequest("GET", "/invocations/ping", nil)
	req.Header.Set("x-agentland-session", "existing-session")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "path", Value: "/ping"}}

	s.handler.Invoke(s.ctx)

	s.Equal(200, s.recorder.Code)
	s.Equal("existing-session", s.recorder.Header().Get("x-agentland-session"))
	s.mockAgentCoreClient.AssertNotCalled(s.T(), "CreateAgentSession")
}
