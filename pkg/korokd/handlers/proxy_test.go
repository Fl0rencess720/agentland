package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProxyByPort_RootPath(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	rt := &captureRoundTripper{statusCode: http.StatusOK}

	router := gin.New()
	group := router.Group("/api")
	InitProxyApi(group, ProxyOptions{
		Transport: rt,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/by-port/5173?a=1&scheme=http", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "/", resp["path"])
	require.Equal(t, "a=1", resp["query"])
	require.Equal(t, "", resp["authorization"])
}

func TestProxyByPort_SubPath(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	rt := &captureRoundTripper{statusCode: http.StatusCreated}

	router := gin.New()
	group := router.Group("/api")
	InitProxyApi(group, ProxyOptions{
		Transport: rt,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/by-port/5173/assets/app.js", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "/assets/app.js", resp["path"])
}

func TestProxyByPort_InvalidPort(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	group := router.Group("/api")
	InitProxyApi(group, ProxyOptions{})

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/by-port/0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestProxyByPort_InvalidScheme(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	group := router.Group("/api")
	InitProxyApi(group, ProxyOptions{})

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/by-port/5173?scheme=ftp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

type captureRoundTripper struct {
	statusCode int
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	respBody, _ := json.Marshal(map[string]any{
		"path":          req.URL.Path,
		"query":         req.URL.RawQuery,
		"authorization": req.Header.Get("Authorization"),
	})
	return &http.Response{
		StatusCode: c.statusCode,
		Body:       io.NopCloser(strings.NewReader(string(respBody))),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}
