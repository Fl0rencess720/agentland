package service

import (
	"net/http"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/service/auth"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/deployment"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/file"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/job"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/project"
	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var ProviderSet = struct{}{}

type HTTPServer struct {
	*http.Server
}

func NewHTTPServer(
	rateLimiter *middlewares.IPRateLimiter,
	authHandler *auth.AuthHandler,
	projectHandler *project.ProjectHandler,
	jobHandler *job.JobHandler,
	deploymentHandler *deployment.DeploymentHandler,
	fileHandler *file.FileHandler,
) *HTTPServer {
	e := gin.New()
	e.Use(
		gin.Logger(),
		gin.Recovery(),
		ginzap.Ginzap(zap.L(), time.RFC3339, false),
		ginzap.RecoveryWithZap(zap.L(), false),
	)

	e.Use(middlewares.Cors())
	e.Use(middlewares.Trace())
	e.Use(middlewares.IPRateLimitMiddleware(rateLimiter))

	app := e.Group("/api/v1", middlewares.Auth())
	{
		auth.InitApi(app.Group("/auth"), authHandler)
		project.InitApi(app.Group("/projects"), projectHandler)
		job.InitApi(app.Group("/jobs"), jobHandler)
		deployment.InitApi(app.Group("/deployments"), deploymentHandler)
		file.InitApi(app.Group("/files"), fileHandler)
	}

	appNoneAuth := e.Group("/api/v1")
	{
		auth.InitNoneAuthApi(appNoneAuth.Group("/auth"), authHandler)
	}

	return &HTTPServer{
		Server: &http.Server{
			Addr:    viper.GetString("server.http.addr"),
			Handler: e,
		},
	}
}
