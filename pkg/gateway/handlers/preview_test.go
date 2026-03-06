package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
)

type mockPreviewStore struct {
	createFn func(ctx context.Context, token string, info *db.PreviewInfo, ttl time.Duration) error
	getFn    func(ctx context.Context, token string) (*db.PreviewInfo, error)
}

func (m *mockPreviewStore) Create(ctx context.Context, token string, info *db.PreviewInfo, ttl time.Duration) error {
	if m.createFn != nil {
		return m.createFn(ctx, token, info, ttl)
	}
	return nil
}

func (m *mockPreviewStore) Get(ctx context.Context, token string) (*db.PreviewInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, token)
	}
	return nil, db.ErrPreviewNotFound
}

func TestPreviewSuite(t *testing.T) {
	suite.Run(t, &PreviewSuite{})
}

type PreviewSuite struct {
	suite.Suite
	recorder *httptest.ResponseRecorder
	ctx      *gin.Context
	handler  *PreviewHandler
}

func (s *PreviewSuite) SetupSuite() {
	gin.SetMode(gin.ReleaseMode)
}

func (s *PreviewSuite) SetupTest() {
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)
	s.handler = &PreviewHandler{
		sessionStore: &mockSessionStore{},
		previewStore: &mockPreviewStore{},
		tokenSigner: &mockTokenSigner{
			signFn: func(sessionID, subject string, version int64) (string, error) {
				return "preview.jwt.token", nil
			},
		},
		proxyEngine: &ProxyEngine{Transport: http.DefaultTransport},
		tokenGenerator: func() string {
			return "pv-token"
		},
		now: func() time.Time {
			return time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
		},
	}
}

func (s *PreviewSuite) TestCreatePreview_Success() {
	var storedToken string
	var storedInfo *db.PreviewInfo
	var storedTTL time.Duration

	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			return &db.SandboxInfo{SandboxID: sandboxID, GrpcEndpoint: "sandbox.test:1883"}, nil
		},
	}
	s.handler.previewStore = &mockPreviewStore{
		createFn: func(ctx context.Context, token string, info *db.PreviewInfo, ttl time.Duration) error {
			storedToken = token
			storedInfo = info
			storedTTL = ttl
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/previews", bytes.NewBufferString(`{"port":3000,"expires_in_seconds":600}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-agentland-session", "session-1")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Host = "gateway.example.com"
	s.ctx.Request = req

	s.handler.CreatePreview(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("pv-token", storedToken)
	s.Equal("session-1", storedInfo.SessionID)
	s.Equal(3000, storedInfo.Port)
	s.Equal(600*time.Second, storedTTL)
	s.Contains(s.recorder.Body.String(), `"preview_token":"pv-token"`)
	s.Contains(s.recorder.Body.String(), `"preview_url":"https://gateway.example.com/p/pv-token/"`)
}

func (s *PreviewSuite) TestCreatePreview_MissingSession() {
	req := httptest.NewRequest(http.MethodPost, "/api/previews", bytes.NewBufferString(`{"port":3000}`))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.handler.CreatePreview(s.ctx)

	s.Equal(http.StatusBadRequest, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"msg":"Form Error"`)
}

func (s *PreviewSuite) TestProxyPreview_Success() {
	s.handler.previewStore = &mockPreviewStore{
		getFn: func(ctx context.Context, token string) (*db.PreviewInfo, error) {
			s.Equal("pv-token", token)
			return &db.PreviewInfo{SessionID: "session-1", Port: 3000}, nil
		},
	}
	s.handler.sessionStore = &mockSessionStore{
		getSessionFn: func(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
			return &db.SandboxInfo{SandboxID: sandboxID, GrpcEndpoint: "sandbox.test:1883"}, nil
		},
	}
	s.handler.proxyEngine.Transport = RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/api/proxy/by-port/3000/assets/app.js", r.URL.Path)
		s.Equal("v=1", r.URL.RawQuery)
		s.Equal("Bearer preview.jwt.token", r.Header.Get("Authorization"))
		s.Equal("session-1", r.Header.Get("x-agentland-session"))
		s.Equal("text/plain", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		s.NoError(err)
		s.Equal("hello", string(body))
		resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`ok`))}
		return resp, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/p/pv-token/assets/app.js?v=1", strings.NewReader("hello"))
	req.Header.Set("Content-Type", "text/plain")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "previewToken", Value: "pv-token"}, {Key: "path", Value: "/assets/app.js"}}

	s.handler.ProxyPreview(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("session-1", s.recorder.Header().Get("x-agentland-session"))
	s.Equal("ok", s.recorder.Body.String())
}

func (s *PreviewSuite) TestProxyPreview_NotFound() {
	s.handler.previewStore = &mockPreviewStore{
		getFn: func(ctx context.Context, token string) (*db.PreviewInfo, error) {
			return nil, db.ErrPreviewNotFound
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/p/pv-token", nil)
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "previewToken", Value: "pv-token"}}

	s.handler.ProxyPreview(s.ctx)

	s.Equal(http.StatusNotFound, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"error":"preview not found"`)
}
