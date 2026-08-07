package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestCORSAllowsConfiguredOriginWithCredentials(t *testing.T) {
	router := corsTestRouter(t, "https://app.example.com, http://localhost:3000")
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/github/start", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, "http://localhost:3000", recorder.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))
	require.Contains(t, recorder.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	require.Contains(t, recorder.Header().Values("Vary"), "Origin")
}

func TestCORSDoesNotExposeUnconfiguredOrigin(t *testing.T) {
	router := corsTestRouter(t, "https://app.example.com")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.Header.Set("Origin", "https://untrusted.example.com")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, recorder.Header().Get("Access-Control-Allow-Credentials"))
	require.Contains(t, recorder.Header().Values("Vary"), "Origin")
}

func TestCORSLeavesSameOriginRequestsUnaffected(t *testing.T) {
	router := corsTestRouter(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/github/start", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSIgnoresWildcardConfigurationWithCredentials(t *testing.T) {
	router := corsTestRouter(t, "*")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	request.Header.Set("Origin", "https://app.example.com")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
	require.Empty(t, recorder.Header().Get("Access-Control-Allow-Credentials"))
}

func corsTestRouter(t *testing.T, origins any) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	viper.Set("server.http.cors.allowed_origins", origins)
	t.Cleanup(func() { viper.Set("server.http.cors.allowed_origins", nil) })
	router := gin.New()
	router.Use(Cors())
	router.Any("/api/v1/*path", func(c *gin.Context) { c.Status(http.StatusOK) })
	return router
}
