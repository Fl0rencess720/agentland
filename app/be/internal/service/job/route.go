package job

import "github.com/gin-gonic/gin"

func InitApi(group *gin.RouterGroup, jobHandler *JobHandler) {
	group.GET("/:job_id", jobHandler.Detail)
}
