package auth

import "github.com/gin-gonic/gin"

func InitApi(group *gin.RouterGroup, authHandler *AuthHandler) {
	group.GET("/me", authHandler.Me)
	group.POST("/logout", authHandler.Logout)
}

func InitNoneAuthApi(group *gin.RouterGroup, authHandler *AuthHandler) {
	group.POST("/github/start", authHandler.GitHubStart)
	group.POST("/github/callback", authHandler.GitHubCallback)
	group.POST("/refresh", authHandler.Refresh)
}
