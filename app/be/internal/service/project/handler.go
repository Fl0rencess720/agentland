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
	req := models.GenerationCreateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.CreateGeneration(c.Request.Context(), principalFromContext(c), c.Param("project_id"), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.MessageResponse(c, "accepted", resp)
}

func (h *ProjectHandler) ListMessages(c *gin.Context) {
	req := models.ChatMessagesReq{}
	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.ListMessages(c.Request.Context(), principalFromContext(c), c.Param("project_id"), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *ProjectHandler) CreateMessage(c *gin.Context) {
	req := models.ChatMessageCreateReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	writeEvent := func(msg string, data any) error {
		body, err := json.Marshal(response.Body{Msg: msg, Code: http.StatusOK, Data: data})
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(c.Writer, "data: %s\n\n", body); err != nil {
			return err
		}
		c.Writer.Flush()
		return nil
	}

	resp, err := h.projectUseCase.CreateMessage(c.Request.Context(), principalFromContext(c), c.Param("project_id"), &req, func(delta string) error {
		return writeEvent("delta", models.ChatMessageStreamDeltaResp{Text: delta})
	})
	if err != nil {
		zap.L().Error("create project chat message failed", zap.Error(err), zap.String("project_id", c.Param("project_id")))
		if streamErr := writeEvent("error", gin.H{"message": err.Error()}); streamErr != nil {
			zap.L().Error("write project chat error event failed", zap.Error(streamErr), zap.String("project_id", c.Param("project_id")))
		}
		return
	}

	if err := writeEvent("done", resp); err != nil {
		zap.L().Error("write project chat done event failed", zap.Error(err), zap.String("project_id", c.Param("project_id")))
	}
}

func (h *ProjectHandler) FileTree(c *gin.Context) {
	req := models.FileTreeReq{}
	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.FileTree(c.Request.Context(), principalFromContext(c), c.Param("project_id"), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *ProjectHandler) FileContent(c *gin.Context) {
	req := models.FileContentReq{}
	if err := c.ShouldBindQuery(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.FileContent(c.Request.Context(), principalFromContext(c), c.Param("project_id"), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func (h *ProjectHandler) Download(c *gin.Context) {
	archive, apiErr := h.projectUseCase.Download(c.Request.Context(), principalFromContext(c), c.Param("project_id"))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	c.Header("Content-Type", archive.ContentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", archive.FileName))
	c.Data(http.StatusOK, archive.ContentType, archive.Content)
}

func (h *ProjectHandler) StartPreview(c *gin.Context) {
	req := models.PreviewStartReq{}
	if err := c.ShouldBindJSON(&req); err != nil {
		zap.L().Error("request bind error", zap.Error(err))
		response.WriteAPIError(c, response.ValidationError(err))
		return
	}
	resp, apiErr := h.projectUseCase.StartPreview(c.Request.Context(), principalFromContext(c), c.Param("project_id"), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.MessageResponse(c, "preview_started", resp)
}

func (h *ProjectHandler) PreviewStatus(c *gin.Context) {
	resp, apiErr := h.projectUseCase.PreviewStatus(c.Request.Context(), principalFromContext(c), c.Param("project_id"))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
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
