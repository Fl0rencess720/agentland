package auth

import (
	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type AuthHandler struct {
	authUseCase biz.AuthUseCase
}

func NewAuthHandler(authUseCase biz.AuthUseCase) *AuthHandler {
	return &AuthHandler{authUseCase: authUseCase}
}

func (h *AuthHandler) GitHubStart(c *gin.Context) {
	req := models.GitHubStartReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Warn("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.authUseCase.GitHubStart(c.Request.Context(), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *AuthHandler) GitHubCallback(c *gin.Context) {
	req := models.GitHubCallbackReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Warn("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.authUseCase.GitHubCallback(c.Request.Context(), &req, c.Request.UserAgent(), c.ClientIP())
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	req := models.RefreshTokenReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Warn("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
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
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Warn("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
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
