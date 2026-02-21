package korokd

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/sandboxtoken"
	"github.com/Fl0rencess720/agentland/pkg/korokd/config"
	"github.com/Fl0rencess720/agentland/pkg/korokd/handlers"
	"github.com/Fl0rencess720/agentland/pkg/korokd/middleware"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type Server struct {
	httpServer *http.Server
}

func NewServer(cfg *config.Config) (*Server, error) {
	s := &Server{}

	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/health", s.HealthHandler)

	verifier, err := sandboxtoken.NewVerifierFromConfig(sandboxtoken.VerifierConfig{
		PublicKeyPath: cfg.SandboxJWTPublicPath,
		Issuer:        cfg.SandboxJWTIssuer,
		Audience:      cfg.SandboxJWTAudience,
		ClockSkew:     cfg.SandboxJWTClockSkew,
	})
	if err != nil {
		return nil, fmt.Errorf("init sandbox token verifier failed: %w", err)
	}

	api := r.Group("/api")
	api.Use(middleware.SandboxAuth(verifier))
	handlers.InitCodeInterpreterApi(api)
	handlers.InitFSApi(api, cfg.WorkspaceRoot, cfg.MaxFileBytes)
	handlers.InitProxyApi(api, handlers.ProxyOptions{})

	s.httpServer = &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s, nil
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			zap.L().Error("Korokd server shutdown error", zap.Error(err))
		}
	}()

	zap.S().Infof("korokd http server listening on %s", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
