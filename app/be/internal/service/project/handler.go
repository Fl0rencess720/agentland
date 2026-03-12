package project

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ProjectHandler struct {
	projectUseCase biz.ProjectUseCase
}

func NewProjectHandler(projectUseCase biz.ProjectUseCase) *ProjectHandler {
	return &ProjectHandler{projectUseCase: projectUseCase}
}

func (h *ProjectHandler) List(c *gin.Context) {
	req := models.ProjectListReq{}
	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.List(c.Request.Context(), principalFromContext(c), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *ProjectHandler) Create(c *gin.Context) {
	req := models.ProjectCreateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.Create(c.Request.Context(), principalFromContext(c), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.MessageResponse(c, "created", resp)
}

func (h *ProjectHandler) Detail(c *gin.Context) {
	resp, apiErr := h.projectUseCase.Detail(c.Request.Context(), principalFromContext(c), c.Param("project_id"))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *ProjectHandler) Update(c *gin.Context) {
	req := models.ProjectUpdateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.Update(c.Request.Context(), principalFromContext(c), c.Param("project_id"), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.MessageResponse(c, "updated", resp)
}

func (h *ProjectHandler) Delete(c *gin.Context) {
	resp, apiErr := h.projectUseCase.Delete(c.Request.Context(), principalFromContext(c), c.Param("project_id"))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.MessageResponse(c, "deleted", resp)
}

func (h *ProjectHandler) Usage(c *gin.Context) {
	resp, apiErr := h.projectUseCase.Usage(c.Request.Context(), principalFromContext(c))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *ProjectHandler) CreateGeneration(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.GenerationCreateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.MessageResponse(c, "accepted", models.GenerationCreateResp{})
}

func (h *ProjectHandler) ListConversations(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	response.SuccessResponse(c, models.ConversationListResp{})
}

func (h *ProjectHandler) ListMessages(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.ChatMessagesReq{}
	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.SuccessResponse(c, models.ChatMessagesResp{ConversationID: req.ConversationID})
}

func (h *ProjectHandler) CreateMessage(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.ChatMessageCreateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	body, err := json.Marshal(response.Body{
		Msg:  "done",
		Code: 200,
		Data: models.ChatMessageStreamDoneResp{},
	})
	if err != nil {
		zap.L().Error("marshal stream response error", zap.Error(err))
		return
	}

	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", body)
	c.Writer.Flush()
}

func (h *ProjectHandler) FileTree(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.FileTreeReq{}
	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.SuccessResponse(c, models.FileTreeResp{Root: req.Path})
}

func (h *ProjectHandler) FileContent(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.FileContentReq{}
	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.SuccessResponse(c, models.FileContentResp{Path: req.Path})
}

func (h *ProjectHandler) Download(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", "attachment; filename=\"project.zip\"")
	c.Status(http.StatusOK)
}

func (h *ProjectHandler) StartPreview(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.PreviewStartReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.MessageResponse(c, "preview_started", models.PreviewStartResp{})
}

func (h *ProjectHandler) PreviewStatus(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	response.SuccessResponse(c, models.PreviewStatusResp{})
}

func (h *ProjectHandler) Publish(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.PublishReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.MessageResponse(c, "published", models.PublishResp{})
}

func (h *ProjectHandler) CreateDeployment(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.DeploymentCreateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.MessageResponse(c, "deployment_started", models.DeploymentCreateResp{})
}

func (h *ProjectHandler) CreateShare(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	req := models.ShareCreateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.ErrorResponse(c, http.StatusBadRequest, "invalid_argument", gin.H{"type": "VALIDATION_ERROR"})
		return
	}

	response.SuccessResponse(c, models.ShareCreateResp{})
}

func (h *ProjectHandler) DeleteShare(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx
	_ = c.Param("share_id")

	response.MessageResponse(c, "deleted", models.ShareDeleteResp{Success: true})
}

func principalFromContext(c *gin.Context) models.AuthPrincipal {
	return models.AuthPrincipal{
		UserID:    c.GetString(string(middlewares.UserIDKey)),
		SessionID: c.GetString(string(middlewares.SessionIDKey)),
	}
}
