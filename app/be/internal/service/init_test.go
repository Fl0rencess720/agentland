package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestUntrustedForwardedIPCannotBypassRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	viper.Set("server.http.trusted_proxies", []string{})
	t.Cleanup(func() { viper.Set("server.http.trusted_proxies", []string{}) })
	router := gin.New()
	configureTrustedProxies(router)
	router.Use(middlewares.IPRateLimitMiddleware(middlewares.NewIPRateLimiter(0, 1)))
	router.GET("/api", func(c *gin.Context) { c.Status(http.StatusOK) })

	first := serveRateLimitedRequest(router, "203.0.113.1")
	second := serveRateLimitedRequest(router, "203.0.113.2")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusTooManyRequests, second.Code)
}

func TestConfiguredProxyCanForwardClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	viper.Set("server.http.trusted_proxies", "192.0.2.10,198.51.100.10")
	t.Cleanup(func() { viper.Set("server.http.trusted_proxies", []string{}) })
	router := gin.New()
	configureTrustedProxies(router)
	router.Use(middlewares.IPRateLimitMiddleware(middlewares.NewIPRateLimiter(0, 1)))
	router.GET("/api", func(c *gin.Context) { c.Status(http.StatusOK) })

	first := serveRateLimitedRequest(router, "203.0.113.1")
	second := serveRateLimitedRequest(router, "203.0.113.2")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
}

func serveRateLimitedRequest(router http.Handler, forwardedFor string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", forwardedFor)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
