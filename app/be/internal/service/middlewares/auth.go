package middlewares

import (
	"net/http"
	"strings"
	"sync"

	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/jwtc"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

type ContextKey string

var (
	UserIDKey    = ContextKey("user_id")
	SessionIDKey = ContextKey("session_id")
	jwtOnce      sync.Once
	jwtManager   *jwtc.Manager
	jwtErr       error
)

func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString := c.GetHeader("Authorization")
		if tokenString == "" {
			response.WriteAPIError(c, response.UnauthorizedError())
			c.Abort()
			return
		}

		parts := strings.Fields(strings.TrimSpace(tokenString))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			response.WriteAPIError(c, response.UnauthorizedError())
			c.Abort()
			return
		}

		manager, err := getJWTManager()
		if err != nil {
			response.ErrorResponse(c, http.StatusInternalServerError, "internal", response.ErrorData{Type: "INTERNAL_ERROR"})
			c.Abort()
			return
		}
		claims, err := manager.VerifyAccessToken(parts[1])
		if err != nil {
			response.WriteAPIError(c, response.UnauthorizedError())
			c.Abort()
			return
		}

		c.Set(string(UserIDKey), claims.Subject)
		c.Set(string(SessionIDKey), claims.SessionID)
		c.Next()
	}
}

func getJWTManager() (*jwtc.Manager, error) {
	jwtOnce.Do(func() {
		jwtManager, jwtErr = jwtc.NewManager(jwtc.Config{
			PrivateKeyPath: viper.GetString("auth.jwt.private_key_path"),
			PublicKeyPath:  viper.GetString("auth.jwt.public_key_path"),
			Issuer:         viper.GetString("auth.jwt.issuer"),
			Audience:       viper.GetString("auth.jwt.audience"),
			TTL:            viper.GetDuration("auth.access_ttl"),
		})
	})
	return jwtManager, jwtErr
}
