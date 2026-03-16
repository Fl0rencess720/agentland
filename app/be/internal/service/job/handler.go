package job

import (
	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/gin-gonic/gin"
)

type JobHandler struct {
	jobUseCase biz.JobUseCase
}

func NewJobHandler(jobUseCase biz.JobUseCase) *JobHandler {
	return &JobHandler{jobUseCase: jobUseCase}
}

func (h *JobHandler) Detail(c *gin.Context) {
	resp, apiErr := h.jobUseCase.Detail(c.Request.Context(), principalFromContext(c), c.Param("job_id"))
	if apiErr != nil {
		response.WriteAPIError(c, apiErr)
		return
	}
	response.SuccessResponse(c, resp)
}

func principalFromContext(c *gin.Context) models.AuthPrincipal {
	return models.AuthPrincipal{
		UserID:    c.GetString(string(middlewares.UserIDKey)),
		SessionID: c.GetString(string(middlewares.SessionIDKey)),
	}
}
