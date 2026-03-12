package deployment

import "github.com/gin-gonic/gin"

func InitApi(group *gin.RouterGroup, deploymentHandler *DeploymentHandler) {
	group.GET("/:deployment_id", deploymentHandler.Detail)
}
