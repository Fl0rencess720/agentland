package project

import "github.com/gin-gonic/gin"

func InitAPI(group *gin.RouterGroup, handler *ProjectHandler) {
	group.GET("", handler.List)
	group.POST("", handler.Create)
	group.GET("/usage", handler.Usage)
	group.GET("/:project_id", handler.Detail)
	group.PATCH("/:project_id", handler.Update)
	group.DELETE("/:project_id", handler.Delete)
	group.POST("/:project_id/runs", handler.CreateRun)
	group.GET("/:project_id/messages", handler.ListMessages)
	group.GET("/:project_id/files/tree", handler.FileTree)
	group.GET("/:project_id/files/content", handler.FileContent)
	group.PUT("/:project_id/files/content", handler.UpdateFileContent)
	group.POST("/:project_id/previews", handler.StartPreview)
	group.GET("/:project_id/preview", handler.Preview)
}

func InitRunAPI(group *gin.RouterGroup, handler *ProjectHandler) {
	group.GET("/:run_id", handler.GetRun)
	group.GET("/:run_id/events", handler.RunEvents)
	group.POST("/:run_id/cancel", handler.CancelRun)
	group.GET("/:run_id/trajectory", handler.RunTrajectory)
	group.POST("/:run_id/replays", handler.ReplayRun)
}
