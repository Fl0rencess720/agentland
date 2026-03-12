package file

import "github.com/gin-gonic/gin"

func InitApi(group *gin.RouterGroup, fileHandler *FileHandler) {
	group.POST("", fileHandler.Upload)
	group.GET("/:file_id", fileHandler.Detail)
}
