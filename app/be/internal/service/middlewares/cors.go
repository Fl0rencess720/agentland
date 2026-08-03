package middlewares

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func Cors() gin.HandlerFunc {
	allowedOrigins := configuredCORSOrigins()
	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		if origin != "" {
			c.Writer.Header().Add("Vary", "Origin")
		}
		if _, allowed := allowedOrigins[origin]; origin != "" && allowed {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, Last-Event-ID")
			c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func configuredCORSOrigins() map[string]struct{} {
	origins := make(map[string]struct{})
	values := viper.GetStringSlice("server.http.cors.allowed_origins")
	if len(values) == 0 {
		values = []string{viper.GetString("server.http.cors.allowed_origins")}
	}
	for _, value := range values {
		for _, origin := range strings.Split(value, ",") {
			origin = strings.TrimSpace(origin)
			if origin != "" && origin != "*" {
				origins[origin] = struct{}{}
			}
		}
	}
	return origins
}
