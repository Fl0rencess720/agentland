package auth

import (
	"net/http"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/httpbind"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

const githubOAuthNonceCookie = "agentland_oauth_nonce"

const maxAuthJSONBodyBytes = 16 << 10

type AuthHandler struct {
	authUseCase biz.AuthUseCase
}

func NewAuthHandler(authUseCase biz.AuthUseCase) *AuthHandler {
	return &AuthHandler{authUseCase: authUseCase}
}

func (h *AuthHandler) GitHubStart(c *gin.Context) {
	req := models.GitHubStartReq{}
	if apiErr := httpbind.JSON(c, &req, maxAuthJSONBodyBytes); apiErr != nil {
		zap.L().Warn("request bind error", zap.Error(apiErr))
		response.WriteAPIError(c, apiErr)
		return
	}
	resp, apiErr := h.authUseCase.GitHubStart(c.Request.Context(), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	setOAuthNonceCookie(c, resp.Nonce)
	response.SuccessResponse(c, resp)
}

func (h *AuthHandler) GitHubCallback(c *gin.Context) {
	req := models.GitHubCallbackReq{}
	req.Nonce, _ = c.Cookie(githubOAuthNonceCookie)
	clearOAuthNonceCookie(c)
	if apiErr := httpbind.JSON(c, &req, maxAuthJSONBodyBytes); apiErr != nil {
		zap.L().Warn("request bind error", zap.Error(apiErr))
		response.WriteAPIError(c, apiErr)
		return
	}
	resp, apiErr := h.authUseCase.GitHubCallback(c.Request.Context(), &req, c.Request.UserAgent(), c.ClientIP())
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func setOAuthNonceCookie(c *gin.Context, nonce string) {
	ttl := viper.GetDuration("auth.oauth_state_ttl")
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     githubOAuthNonceCookie,
		Value:    nonce,
		Path:     "/api/v1/auth/github/callback",
		MaxAge:   max(1, int(ttl.Seconds())),
		Expires:  time.Now().UTC().Add(ttl),
		HttpOnly: true,
		Secure:   c.Request.TLS != nil || viper.GetBool("auth.oauth_cookie_secure"),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearOAuthNonceCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     githubOAuthNonceCookie,
		Path:     "/api/v1/auth/github/callback",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
		HttpOnly: true,
		Secure:   c.Request.TLS != nil || viper.GetBool("auth.oauth_cookie_secure"),
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	req := models.RefreshTokenReq{}
	if apiErr := httpbind.JSON(c, &req, maxAuthJSONBodyBytes); apiErr != nil {
		zap.L().Warn("request bind error", zap.Error(apiErr))
		response.WriteAPIError(c, apiErr)
		return
	}
	resp, apiErr := h.authUseCase.Refresh(c.Request.Context(), &req, c.Request.UserAgent(), c.ClientIP())
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *AuthHandler) Me(c *gin.Context) {
	resp, apiErr := h.authUseCase.Me(c.Request.Context(), principalFromContext(c))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	req := models.LogoutReq{}
	if apiErr := httpbind.JSON(c, &req, maxAuthJSONBodyBytes); apiErr != nil {
		zap.L().Warn("request bind error", zap.Error(apiErr))
		response.WriteAPIError(c, apiErr)
		return
	}
	resp, apiErr := h.authUseCase.Logout(c.Request.Context(), principalFromContext(c), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.MessageResponse(c, "logged_out", resp)
}

func principalFromContext(c *gin.Context) models.AuthPrincipal {
	return models.AuthPrincipal{
		UserID:    c.GetString(string(middlewares.UserIDKey)),
		SessionID: c.GetString(string(middlewares.SessionIDKey)),
	}
}
