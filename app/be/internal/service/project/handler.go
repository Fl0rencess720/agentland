package project

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/httpbind"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/gin-gonic/gin"
)

type ProjectHandler struct{ usecase biz.ProjectUseCase }

const (
	maxProjectJSONBodyBytes = 64 << 10
	maxRunJSONBodyBytes     = models.MaxRunMessageBytes*6 + 1024
	maxFileJSONBodyBytes    = models.MaxFileContentBytes*6 + 1024
)

func NewProjectHandler(usecase biz.ProjectUseCase) *ProjectHandler {
	return &ProjectHandler{usecase: usecase}
}

func (h *ProjectHandler) List(c *gin.Context) {
	var req models.ProjectListReq
	if !bindQuery(c, &req) {
		return
	}
	data, apiErr := h.usecase.List(c.Request.Context(), principal(c), &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) Create(c *gin.Context) {
	var req models.ProjectCreateReq
	if !bindJSON(c, &req, maxProjectJSONBodyBytes) {
		return
	}
	data, apiErr := h.usecase.Create(c.Request.Context(), principal(c), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.MessageResponse(c, "created", data)
}

func (h *ProjectHandler) Detail(c *gin.Context) {
	data, apiErr := h.usecase.Detail(c.Request.Context(), principal(c), c.Param("project_id"))
	write(c, data, apiErr)
}

func (h *ProjectHandler) Update(c *gin.Context) {
	var req models.ProjectUpdateReq
	if !bindJSON(c, &req, maxProjectJSONBodyBytes) {
		return
	}
	data, apiErr := h.usecase.Update(c.Request.Context(), principal(c), c.Param("project_id"), &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) Delete(c *gin.Context) {
	data, apiErr := h.usecase.Delete(c.Request.Context(), principal(c), c.Param("project_id"))
	write(c, data, apiErr)
}

func (h *ProjectHandler) Usage(c *gin.Context) {
	data, apiErr := h.usecase.Usage(c.Request.Context(), principal(c))
	write(c, data, apiErr)
}

func (h *ProjectHandler) CreateRun(c *gin.Context) {
	var req models.RunCreateReq
	if !bindJSON(c, &req, maxRunJSONBodyBytes) {
		return
	}
	data, apiErr := h.usecase.CreateRun(c.Request.Context(), principal(c), c.Param("project_id"), c.GetHeader("Idempotency-Key"), &req)
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.AcceptedResponse(c, data)
}

func (h *ProjectHandler) GetRun(c *gin.Context) {
	data, apiErr := h.usecase.GetRun(c.Request.Context(), principal(c), c.Param("run_id"))
	write(c, data, apiErr)
}

func (h *ProjectHandler) RunEvents(c *gin.Context) {
	if _, apiErr := h.usecase.GetRun(c.Request.Context(), principal(c), c.Param("run_id")); apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()
	_ = h.usecase.StreamRunEvents(c.Request.Context(), principal(c), c.Param("run_id"), c.GetHeader("Last-Event-ID"), func(event *models.StoredRunEvent) error {
		if event.Type == "" {
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return err
			}
			c.Writer.Flush()
			return nil
		}
		if _, err := fmt.Fprintf(c.Writer, "id: %s\nevent: %s\ndata: %s\n\n", sanitizeSSEField(event.ID), sanitizeSSEField(event.Type), event.Data); err != nil {
			return err
		}
		c.Writer.Flush()
		return nil
	})
}

func (h *ProjectHandler) CancelRun(c *gin.Context) {
	data, apiErr := h.usecase.CancelRun(c.Request.Context(), principal(c), c.Param("run_id"))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.AcceptedResponse(c, data)
}

func (h *ProjectHandler) RunTrajectory(c *gin.Context) {
	data, apiErr := h.usecase.RunTrajectory(c.Request.Context(), principal(c), c.Param("run_id"))
	write(c, data, apiErr)
}

func (h *ProjectHandler) ReplayRun(c *gin.Context) {
	var req models.ReplayRunReq
	if !bindJSON(c, &req, maxProjectJSONBodyBytes) {
		return
	}
	data, apiErr := h.usecase.ReplayRun(c.Request.Context(), principal(c), c.Param("run_id"), &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) ListMessages(c *gin.Context) {
	var req models.MessageListReq
	if !bindQuery(c, &req) {
		return
	}
	data, apiErr := h.usecase.ListMessages(c.Request.Context(), principal(c), c.Param("project_id"), &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) FileTree(c *gin.Context) {
	var req models.FileTreeReq
	if !bindQuery(c, &req) {
		return
	}
	data, apiErr := h.usecase.FileTree(c.Request.Context(), principal(c), c.Param("project_id"), &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) FileContent(c *gin.Context) {
	var req models.FileContentReq
	if !bindQuery(c, &req) {
		return
	}
	data, apiErr := h.usecase.FileContent(c.Request.Context(), principal(c), c.Param("project_id"), &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) UpdateFileContent(c *gin.Context) {
	var query models.FileContentReq
	if !bindQuery(c, &query) {
		return
	}
	var req models.FileContentUpdateReq
	if !bindJSON(c, &req, maxFileJSONBodyBytes) {
		return
	}
	data, apiErr := h.usecase.UpdateFileContent(c.Request.Context(), principal(c), c.Param("project_id"), &query, &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) StartPreview(c *gin.Context) {
	var req models.PreviewStartReq
	if !bindJSON(c, &req, maxProjectJSONBodyBytes) {
		return
	}
	data, apiErr := h.usecase.StartPreview(c.Request.Context(), principal(c), c.Param("project_id"), &req)
	write(c, data, apiErr)
}

func (h *ProjectHandler) Preview(c *gin.Context) {
	data, apiErr := h.usecase.Preview(c.Request.Context(), principal(c), c.Param("project_id"))
	write(c, data, apiErr)
}

func principal(c *gin.Context) models.AuthPrincipal {
	return models.AuthPrincipal{UserID: c.GetString(string(middlewares.UserIDKey)), SessionID: c.GetString(string(middlewares.SessionIDKey))}
}

func bindJSON(c *gin.Context, target any, maxBytes int64) bool {
	if apiErr := httpbind.JSON(c, target, maxBytes); apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return false
	}
	return true
}

func bindQuery(c *gin.Context, target any) bool {
	if err := c.ShouldBindQuery(target); err != nil {
		response.WriteAPIError(c, response.ValidationError(err))
		return false
	}
	return true
}

func write(c *gin.Context, data any, apiErr *response.APIError) {
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, data)
}

func sanitizeSSEField(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r", ""), "\n", "")
}
