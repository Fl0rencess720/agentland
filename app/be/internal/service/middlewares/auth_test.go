package middlewares

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/jwtc"
	"github.com/Fl0rencess720/agentland/pkg/common/testutil"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	privatePath, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)
	viper.Set("auth.jwt.private_key_path", privatePath)
	viper.Set("auth.jwt.public_key_path", publicPath)
	viper.Set("auth.jwt.issuer", "agentland-app-be")
	viper.Set("auth.jwt.audience", "agentland-app")
	viper.Set("auth.access_ttl", 15*time.Minute)

	jwtOnce = sync.Once{}
	jwtManager = nil
	jwtErr = nil

	manager, err := jwtc.NewManager(jwtc.Config{PrivateKeyPath: privatePath, PublicKeyPath: publicPath, Issuer: "agentland-app-be", Audience: "agentland-app", TTL: 15 * time.Minute})
	require.NoError(t, err)
	token, _, err := manager.SignAccessToken("u_123", "sess_123", time.Now())
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	ctx, router := gin.CreateTestContext(recorder)
	router.Use(Auth())
	router.GET("/me", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user_id": c.GetString(string(UserIDKey)), "session_id": c.GetString(string(SessionIDKey))})
	})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/me", nil)
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	router.HandleContext(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"user_id":"u_123","session_id":"sess_123"}`, recorder.Body.String())
}
