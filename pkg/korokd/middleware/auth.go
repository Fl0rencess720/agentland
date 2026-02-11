package middleware

import (
	"net/http"

	"github.com/Fl0rencess720/agentland/pkg/common/sandboxtoken"
	"github.com/gin-gonic/gin"
)

const claimsContextKey = "sandboxJWTClaims"

type tokenVerifier interface {
	Verify(token string) (*sandboxtoken.Claims, error)
}

func SandboxAuth(verifier tokenVerifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		if verifier == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sandbox auth verifier is not configured"})
			return
		}

		token, err := sandboxtoken.ParseBearerToken(c.GetHeader("Authorization"))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid authorization header"})
			return
		}

		claims, err := verifier.Verify(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid sandbox token"})
			return
		}

		c.Set(claimsContextKey, claims)
		c.Next()
	}
}

func ClaimsFromContext(c *gin.Context) (*sandboxtoken.Claims, bool) {
	v, ok := c.Get(claimsContextKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*sandboxtoken.Claims)
	return claims, ok
}
