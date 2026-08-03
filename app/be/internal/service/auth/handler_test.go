package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type authUseCaseStub struct {
	callbackReq *models.GitHubCallbackReq
	startCalls  int
}

func (s *authUseCaseStub) GitHubStart(context.Context, *models.GitHubStartReq) (*models.GitHubStartResp, *response.APIError) {
	s.startCalls++
	return &models.GitHubStartResp{AuthorizeURL: "https://github.example/authorize", State: "state-1", Nonce: "nonce-1"}, nil
}

func (s *authUseCaseStub) GitHubCallback(_ context.Context, req *models.GitHubCallbackReq, _, _ string) (*models.GitHubCallbackResp, *response.APIError) {
	s.callbackReq = req
	return &models.GitHubCallbackResp{User: models.UserProfile{ID: "user-1"}}, nil
}

func (s *authUseCaseStub) Refresh(context.Context, *models.RefreshTokenReq, string, string) (*models.RefreshTokenResp, *response.APIError) {
	return &models.RefreshTokenResp{}, nil
}

func (s *authUseCaseStub) Me(context.Context, models.AuthPrincipal) (*models.CurrentUserResp, *response.APIError) {
	return &models.CurrentUserResp{}, nil
}

func (s *authUseCaseStub) Logout(context.Context, models.AuthPrincipal, *models.LogoutReq) (*models.LogoutResp, *response.APIError) {
	return &models.LogoutResp{Success: true}, nil
}

func TestOAuthNonceCookieBindsStartAndCallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	viper.Set("auth.oauth_cookie_secure", true)
	t.Cleanup(func() { viper.Set("auth.oauth_cookie_secure", false) })
	usecase := &authUseCaseStub{}
	handler := NewAuthHandler(usecase)
	router := gin.New()
	router.POST("/api/v1/auth/github/start", handler.GitHubStart)
	router.POST("/api/v1/auth/github/callback", handler.GitHubCallback)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/api/v1/auth/github/start", strings.NewReader(`{"redirect_uri":"https://app.example/login"}`)))
	require.Equal(t, http.StatusOK, start.Code)
	require.NotContains(t, start.Body.String(), "nonce-1")
	cookies := start.Result().Cookies()
	require.Len(t, cookies, 1)
	nonceCookie := cookies[0]
	require.Equal(t, githubOAuthNonceCookie, nonceCookie.Name)
	require.Equal(t, "nonce-1", nonceCookie.Value)
	require.Equal(t, "/api/v1/auth/github/callback", nonceCookie.Path)
	require.True(t, nonceCookie.HttpOnly)
	require.True(t, nonceCookie.Secure)
	require.Equal(t, http.SameSiteLaxMode, nonceCookie.SameSite)

	callback := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/github/callback", strings.NewReader(`{"code":"code-1","state":"state-1"}`))
	request.AddCookie(nonceCookie)
	router.ServeHTTP(callback, request)
	require.Equal(t, http.StatusOK, callback.Code)
	require.NotNil(t, usecase.callbackReq)
	require.Equal(t, "nonce-1", usecase.callbackReq.Nonce)
	cleared := callback.Result().Cookies()
	require.Len(t, cleared, 1)
	require.Less(t, cleared[0].MaxAge, 0)
}

func TestOAuthStartRejectsOversizedJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usecase := &authUseCaseStub{}
	handler := NewAuthHandler(usecase)
	router := gin.New()
	router.POST("/api/v1/auth/github/start", handler.GitHubStart)
	recorder := httptest.NewRecorder()
	body := `{"redirect_uri":"` + strings.Repeat("x", maxAuthJSONBodyBytes) + `"}`
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/github/start", strings.NewReader(body)))
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Zero(t, usecase.startCalls)
}
