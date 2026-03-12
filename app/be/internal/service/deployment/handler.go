package deployment

import (
	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/gin-gonic/gin"
)

type DeploymentHandler struct {
	deploymentUseCase biz.DeploymentUseCase
}

func NewDeploymentHandler(deploymentUseCase biz.DeploymentUseCase) *DeploymentHandler {
	return &DeploymentHandler{deploymentUseCase: deploymentUseCase}
}

func (h *DeploymentHandler) Detail(c *gin.Context) {
	ctx := c.Request.Context()
	_ = ctx

	response.SuccessResponse(c, models.DeploymentStatusResp{DeploymentID: c.Param("deployment_id")})
}
