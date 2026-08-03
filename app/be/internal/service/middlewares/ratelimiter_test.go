package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestRateLimiterProtectsPublicPreviewAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(IPRateLimitMiddleware(NewIPRateLimiter(0, 0)))
	router.GET("/p/*path", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/p/token/app.js", nil))
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
}

func TestPreviewRateLimiterAllowsFrontendAssetBursts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	viper.Set("rate_limit.preview.requests_per_second", 0.001)
	viper.Set("rate_limit.preview.burst", 64)
	t.Cleanup(func() {
		viper.Set("rate_limit.preview.requests_per_second", nil)
		viper.Set("rate_limit.preview.burst", nil)
	})
	router := gin.New()
	router.GET("/p/*path", IPRateLimitMiddleware(NewPreviewIPRateLimiter()), func(c *gin.Context) { c.Status(http.StatusOK) })

	for requestNumber := range 64 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/p/token/module.js", nil)
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, "preview asset request %d", requestNumber+1)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/p/token/module.js", nil))
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
}

func TestRateLimiterRemovesIdleVisitors(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	limiter := NewIPRateLimiter(1, 1)
	limiter.ttl = time.Minute
	limiter.cleanupInterval = time.Second
	limiter.now = func() time.Time { return now }
	first := limiter.GetLimiter("192.0.2.1")

	now = now.Add(2 * time.Minute)
	limiter.GetLimiter("192.0.2.2")
	second := limiter.GetLimiter("192.0.2.1")

	require.NotSame(t, first, second)
	require.Len(t, limiter.ips, 2)
}
