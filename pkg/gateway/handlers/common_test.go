package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
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
	s.ctx.Request.Body = errReadCloser{err: fmt.Errorf("boom")}

	body, ok := readRequestBody(s.ctx)
	s.False(ok)
	s.Nil(body)
	s.Equal(http.StatusBadRequest, s.recorder.Code)
}

func (s *CommonSuite) TestReadRequestBodyRejectsOversizedPayload() {
	s.ctx.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(strings.Repeat("x", 17)))

	body, ok := readRequestBodyLimit(s.ctx, 16)
	s.False(ok)
	s.Nil(body)
	s.Equal(http.StatusRequestEntityTooLarge, s.recorder.Code)
}

func (s *CommonSuite) TestBindJSONWithBody() {
	s.ctx.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"language":"python","cwd":"/workspace"}`))

	var req models.CreateContextReq
	body, ok := bindJSONWithBody(s.ctx, &req)
	s.True(ok)
	s.Equal("python", req.Language)
	s.Equal("/workspace", req.CWD)
	s.Equal(`{"language":"python","cwd":"/workspace"}`, string(body))

	restored, err := io.ReadAll(s.ctx.Request.Body)
	s.NoError(err)
	s.Equal(`{"language":"python","cwd":"/workspace"}`, string(restored))
}

func (s *CommonSuite) TestBindJSONWithBodyInvalidJSON() {
	s.ctx.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"language":`))

	var req models.CreateContextReq
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

func (s *CommonSuite) TestRewritePrefixedResponseRewritesViteAssets() {
	body := `<script type="module" src="/@vite/client"></script><link href='/src/app.css'>`
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}, "Location": []string{"/next"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	s.NoError(rewritePrefixedResponse(response, "/p/token-1"))
	data, err := io.ReadAll(response.Body)
	s.NoError(err)
	s.Equal(`<script type="module" src="/p/token-1/@vite/client"></script><link href='/p/token-1/src/app.css'>`, string(data))
	s.Equal("/p/token-1/next", response.Header.Get("Location"))
}

func (s *CommonSuite) TestRewritePrefixedResponseRewritesJavaScriptAndCSS() {
	javascript := &http.Response{Header: http.Header{"Content-Type": []string{"text/javascript"}}, Body: io.NopCloser(strings.NewReader(`import "/src/main.tsx"; fetch('/api/items')`))}
	s.NoError(rewritePrefixedResponse(javascript, "/p/token-1/"))
	data, err := io.ReadAll(javascript.Body)
	s.NoError(err)
	s.Equal(`import "/p/token-1/src/main.tsx"; fetch('/p/token-1/api/items')`, string(data))

	stylesheet := &http.Response{Header: http.Header{"Content-Type": []string{"text/css"}}, Body: io.NopCloser(strings.NewReader(`body{background:url(/assets/bg.png)}`))}
	s.NoError(rewritePrefixedResponse(stylesheet, "/p/token-1/"))
	data, err = io.ReadAll(stylesheet.Body)
	s.NoError(err)
	s.Equal(`body{background:url(/p/token-1/assets/bg.png)}`, string(data))
}

func (s *CommonSuite) TestProxyEngineAddsPreviewCORSHeaders() {
	engine := &ProxyEngine{Transport: commonRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/javascript"}, "Access-Control-Allow-Credentials": []string{"true"}},
			Body:       io.NopCloser(strings.NewReader(`export default true`)),
			Request:    request,
		}, nil
	})}
	target, err := url.Parse("http://sandbox.test:1883")
	s.NoError(err)
	s.ctx.Request = httptest.NewRequest(http.MethodGet, "/p/token-1/src/main.ts", nil)
	s.ctx.Request.Header.Set("Origin", "null")

	engine.Forward(s.ctx, ProxyConfig{
		Target:             target,
		Method:             http.MethodGet,
		InternalPath:       "/api/proxy/by-port/3000/src/main.ts",
		ResponsePathPrefix: "/p/token-1",
	})

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Equal("*", s.recorder.Header().Get("Access-Control-Allow-Origin"))
	s.Contains(s.recorder.Header().Get("Access-Control-Allow-Methods"), http.MethodGet)
	s.Empty(s.recorder.Header().Get("Access-Control-Allow-Credentials"))
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
