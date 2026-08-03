package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPreviewProxyPreservesRequestAndResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/p/token/app.js", request.URL.Path)
		require.Equal(t, "v=1", request.URL.RawQuery)
		require.Empty(t, request.Header.Get("Authorization"))
		require.Empty(t, request.Header.Get("Cookie"))
		require.Empty(t, request.Header.Get("X-Agentland-Session"))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.Equal(t, "payload", string(body))
		response := &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("preview body")), Request: request}
		response.Header.Set("Content-Type", "application/javascript")
		response.Header.Set("Connection", "close")
		response.Header.Add("Set-Cookie", "preview=unsafe")
		return response, nil
	})
	router := gin.New()
	activity := &previewActivityStub{}
	router.Any("/p/*path", newPreviewProxy("http://gateway.local", "http://{token}.localhost:18081", transport, activity))
	request := httptest.NewRequest(http.MethodPost, "/p/token/app.js?v=1", strings.NewReader("payload"))
	request.Host = "token.localhost:18081"
	request.Header.Set("Authorization", "Bearer app-token")
	request.Header.Set("Cookie", "app=session")
	request.Header.Set("X-Agentland-Session", "untrusted-session")
	recorder := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusCreated, recorder.Code)
	require.Equal(t, "application/javascript", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Get("Connection"))
	require.Empty(t, recorder.Header().Get("Set-Cookie"))
	require.Equal(t, "sandbox allow-scripts allow-forms allow-popups allow-same-origin", recorder.Header().Get("Content-Security-Policy"))
	require.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, recorder.Header().Get("Access-Control-Allow-Credentials"))
	require.Equal(t, "no-referrer", recorder.Header().Get("Referrer-Policy"))
	require.Equal(t, "preview body", recorder.Body.String())
	require.Equal(t, "token", activity.token)
	require.Equal(t, 1, activity.calls)
}

func TestPreviewProxyDoesNotRenewAfterGatewayError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("missing")), Request: request}, nil
	})
	activity := &previewActivityStub{}
	router := gin.New()
	router.Any("/p/*path", newPreviewProxy("http://gateway.local", "http://{token}.localhost:18081", transport, activity))
	recorder := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
	request := httptest.NewRequest(http.MethodGet, "/p/token/", nil)
	request.Host = "token.localhost:18081"
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Zero(t, activity.calls)
}

func TestPreviewProxyRejectsMainApplicationOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unsafe")), Request: request}, nil
	})
	router := gin.New()
	router.Any("/p/*path", newPreviewProxy("http://gateway.local", "http://{token}.localhost:18081", transport, nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/p/token/", nil)
	request.Host = "localhost:18081"
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusMisdirectedRequest, recorder.Code)
	require.False(t, called)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func (r *closeNotifyRecorder) CloseNotify() <-chan bool { return r.closed }

type previewActivityStub struct {
	token string
	calls int
}

func (s *previewActivityStub) TouchRuntimeByPreviewToken(_ context.Context, token string, _ time.Time) error {
	s.token = token
	s.calls++
	return nil
}
