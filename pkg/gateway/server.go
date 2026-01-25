package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/handlers"
	ginZap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"k8s.io/klog/v2"
)

type Server struct {
	httpServer *http.Server
}

func NewServer(cfg *config.Config) (*Server, error) {
	e := gin.New()
	e.Use(gin.Logger(), gin.Recovery(), ginZap.Ginzap(zap.L(), time.RFC3339, false), ginZap.RecoveryWithZap(zap.L(), false))

	app := e.Group("/api")
	{
		handlers.InitCodeRunnerApi(app.Group("/code-runner"))
	}

	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: e,
	}

	return &Server{httpServer: httpServer}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			klog.Errorf("Server shutdown error: %v", err)
		}
	}()

	zap.S().Infof("Gateway server listening on %s", s.httpServer.Addr)

	return s.httpServer.ListenAndServe()
}
