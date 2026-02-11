package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/sandboxtoken"
	"github.com/Fl0rencess720/agentland/pkg/common/testutil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSandboxAuth_RejectMissingToken(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	_, verifier := newSignerAndVerifier(t)
	router := gin.New()
	router.Use(SandboxAuth(verifier))
	router.POST("/api/execute", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/execute", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestSandboxAuth_RejectInvalidToken(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	_, verifier := newSignerAndVerifier(t)
	router := gin.New()
	router.Use(SandboxAuth(verifier))
	router.POST("/api/execute", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/execute", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.value")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestSandboxAuth_AcceptsValidToken(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)

	signer, verifier := newSignerAndVerifier(t)
	token, err := signer.Sign("session-1", "", 0)
	require.NoError(t, err)

	router := gin.New()
	router.Use(SandboxAuth(verifier))
	router.POST("/api/execute", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/execute", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func newSignerAndVerifier(t *testing.T) (*sandboxtoken.Signer, *sandboxtoken.Verifier) {
	t.Helper()

	privatePath, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)
	signer, err := sandboxtoken.NewSignerFromConfig(sandboxtoken.SignerConfig{
		PrivateKeyPath: privatePath,
		Issuer:         "agentland-gateway",
		Audience:       "sandbox",
		TTL:            5 * time.Minute,
	})
	require.NoError(t, err)

	verifier, err := sandboxtoken.NewVerifierFromConfig(sandboxtoken.VerifierConfig{
		PublicKeyPath: publicPath,
		Issuer:        "agentland-gateway",
		Audience:      "sandbox",
		ClockSkew:     30 * time.Second,
	})
	require.NoError(t, err)
	return signer, verifier
}
