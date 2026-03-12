package job

import (
	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/gin-gonic/gin"
)

type JobHandler struct {
	jobUseCase biz.JobUseCase
}

func NewJobHandler(jobUseCase biz.JobUseCase) *JobHandler {
	return &JobHandler{jobUseCase: jobUseCase}
}

func (h *JobHandler) Detail(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	response.SuccessResponse(c, models.JobStatusResp{JobID: c.Param("job_id")})
}
