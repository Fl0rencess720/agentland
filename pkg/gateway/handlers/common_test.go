package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/Fl0rencess720/agentland/pkg/common/testutil"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

func TestCommonSuite(t *testing.T) {
	suite.Run(t, &CommonSuite{})
}

type CommonSuite struct {
	suite.Suite
	recorder *httptest.ResponseRecorder
	ctx      *gin.Context
}

type commonRoundTripFunc func(*http.Request) (*http.Response, error)

func (f commonRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errReadCloser struct {
	err error
}

func (e errReadCloser) Read(_ []byte) (int, error) {
	return 0, e.err
}

func (e errReadCloser) Close() error {
	return nil
}

func (s *CommonSuite) SetupSuite() {
	gin.SetMode(gin.ReleaseMode)
	zap.ReplaceGlobals(zap.NewNop())
}

func (s *CommonSuite) SetupTest() {
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)
}

func (s *CommonSuite) TestResolveSandboxTarget() {
	target, err := resolveSandboxTarget("sandbox.test:1883")
	s.NoError(err)
	s.Equal("http://sandbox.test:1883", target.String())

	target, err = resolveSandboxTarget("https://sandbox.test:1883")
	s.NoError(err)
	s.Equal("https://sandbox.test:1883", target.String())

	_, err = resolveSandboxTarget("  ")
	s.Error(err)
}

func (s *CommonSuite) TestInitRequestContext() {
	s.ctx.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	s.ctx.Request = s.ctx.Request.WithContext(observability.ContextWithRequestID(context.Background(), "req-123"))

	_, requestID := initRequestContext(s.ctx)
	s.Equal("req-123", requestID)
	s.Equal("req-123", s.recorder.Header().Get(observability.RequestIDHeader))
}

func (s *CommonSuite) TestReadRequestBody() {
	s.ctx.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`))

	body, ok := readRequestBody(s.ctx)
	s.True(ok)
	s.Equal(`{"a":1}`, string(body))
}

func (s *CommonSuite) TestReadRequestBodyError() {
	s.ctx.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
	s.ctx.Request.Body = errReadCloser{err: errors.New("boom")}

	body, ok := readRequestBody(s.ctx)
	s.False(ok)
	s.Nil(body)
	s.Equal(http.StatusBadRequest, s.recorder.Code)
}

func (s *CommonSuite) TestBindJSONWithBody() {
	s.ctx.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"language":"python"}`))

	var req CreateSandboxReq
	body, ok := bindJSONWithBody(s.ctx, &req)
	s.True(ok)
	s.Equal("python", req.Language)
	s.Equal(`{"language":"python"}`, string(body))

	restored, err := io.ReadAll(s.ctx.Request.Body)
	s.NoError(err)
	s.Equal(`{"language":"python"}`, string(restored))
}

func (s *CommonSuite) TestBindJSONWithBodyInvalidJSON() {
	s.ctx.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"language":`))

	var req CreateSandboxReq
	body, ok := bindJSONWithBody(s.ctx, &req)
	s.False(ok)
	s.Nil(body)
	s.Equal(http.StatusBadRequest, s.recorder.Code)
}

func (s *CommonSuite) TestProxyEngineForward() {
	var capturedMethod string
	var capturedPath string
	var capturedQuery string
	var capturedAuth string
	var capturedSession string
	var capturedBody string

	engine := &ProxyEngine{
		Transport: commonRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			capturedMethod = r.Method
			capturedPath = r.URL.Path
			capturedQuery = r.URL.RawQuery
			capturedAuth = r.Header.Get("Authorization")
			capturedSession = r.Header.Get(SessionHeader)
			bodyBytes, err := io.ReadAll(r.Body)
			s.NoError(err)
			capturedBody = string(bodyBytes)

			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}

	s.ctx.Request = httptest.NewRequest(http.MethodGet, "/from-gw?trace=1", strings.NewReader(`{"k":"v"}`))

	target, err := url.Parse("http://sandbox.test:1883")
	s.NoError(err)

	engine.Forward(s.ctx, ProxyConfig{
		Target:       target,
		Method:       http.MethodPost,
		InternalPath: "/api/contexts",
		Body:         []byte(`{"k":"v"}`),
		SessionID:    "session-1",
		SandboxToken: "token-1",
		RequestID:    "req-1",
	})

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal(http.MethodPost, capturedMethod)
	s.Equal("/api/contexts", capturedPath)
	s.Equal("trace=1", capturedQuery)
	s.Equal("Bearer token-1", capturedAuth)
	s.Equal("session-1", capturedSession)
	s.Equal(`{"k":"v"}`, capturedBody)
	s.Equal("session-1", s.recorder.Header().Get(SessionHeader))
}

func (s *CommonSuite) TestBuildTokenSigner() {
	privatePath, _, err := testutil.WriteTestRSAKeys(s.T().TempDir())
	s.NoError(err)

	cfg := &config.Config{
		SandboxJWTPrivatePath: privatePath,
		SandboxJWTIssuer:      "agentland-gateway",
		SandboxJWTAudience:    "sandbox",
		SandboxJWTTTL:         5 * time.Minute,
		SandboxJWTKID:         "default",
	}

	signer, err := BuildTokenSigner(cfg)
	s.NoError(err)

	token, err := signer.Sign("session-1", "", 0)
	s.NoError(err)
	s.NotEmpty(token)
}

func (s *CommonSuite) TestCloseNotifySafeWriter() {
	w := closeNotifySafeWriter{ResponseWriter: s.ctx.Writer}
	s.Nil(w.CloseNotify())
}
