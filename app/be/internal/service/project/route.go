package project

import "github.com/gin-gonic/gin"

func InitApi(group *gin.RouterGroup, projectHandler *ProjectHandler) {
	group.GET("", projectHandler.List)
	group.POST("", projectHandler.Create)
	group.GET("/usage", projectHandler.Usage)
	group.GET("/:project_id", projectHandler.Detail)
	group.PATCH("/:project_id", projectHandler.Update)
	group.DELETE("/:project_id", projectHandler.Delete)
	group.POST("/:project_id/generations", projectHandler.CreateGeneration)
	group.GET("/:project_id/chat/messages", projectHandler.ListMessages)
	group.POST("/:project_id/chat/messages", projectHandler.CreateMessage)
	group.GET("/:project_id/files/tree", projectHandler.FileTree)
	group.GET("/:project_id/files/content", projectHandler.FileContent)
	group.GET("/:project_id/download", projectHandler.Download)
	group.POST("/:project_id/preview/start", projectHandler.StartPreview)
	group.GET("/:project_id/preview", projectHandler.PreviewStatus)
	group.POST("/:project_id/publish", projectHandler.Publish)
	group.POST("/:project_id/deployments", projectHandler.CreateDeployment)
	group.POST("/:project_id/shares", projectHandler.CreateShare)
	group.DELETE("/:project_id/shares/:share_id", projectHandler.DeleteShare)
}
