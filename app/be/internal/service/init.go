package service

import (
	"net/http"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/auth"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/project"
	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type HTTPServer struct {
	*http.Server
}

func NewHTTPServer(
	rateLimiter *middlewares.IPRateLimiter,
	authHandler *auth.AuthHandler,
	projectHandler *project.ProjectHandler,
	runRepo biz.RunWorkerRepo,
) *HTTPServer {
	e := gin.New()
	configureTrustedProxies(e)
	e.Use(
		ginzap.Ginzap(zap.L(), time.RFC3339, false),
		ginzap.RecoveryWithZap(zap.L(), false),
	)

	e.Use(middlewares.Cors())
	e.Use(middlewares.Trace())
	e.Any("/p/*path", middlewares.IPRateLimitMiddleware(middlewares.NewPreviewIPRateLimiter()), PreviewProxy(runRepo))

	app := e.Group("/api/v1", middlewares.IPRateLimitMiddleware(rateLimiter), middlewares.Auth())
	{
		auth.InitApi(app.Group("/auth"), authHandler)
		project.InitAPI(app.Group("/projects"), projectHandler)
		project.InitRunAPI(app.Group("/runs"), projectHandler)
		project.InitPublicationAPI(app.Group("/publications"), projectHandler)
	}

	appNoneAuth := e.Group("/api/v1", middlewares.IPRateLimitMiddleware(rateLimiter))
	{
		auth.InitNoneAuthApi(appNoneAuth.Group("/auth"), authHandler)
	}

	return &HTTPServer{
		Server: &http.Server{
			Addr:              viper.GetString("server.http.addr"),
			Handler:           e,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
}

func configureTrustedProxies(engine *gin.Engine) {
	proxies := viper.GetStringSlice("server.http.trusted_proxies")
	if len(proxies) == 1 && strings.Contains(proxies[0], ",") {
		proxies = strings.FieldsFunc(proxies[0], func(r rune) bool { return r == ',' })
		for index := range proxies {
			proxies[index] = strings.TrimSpace(proxies[index])
		}
	}
	if len(proxies) == 0 {
		_ = engine.SetTrustedProxies(nil)
		return
	}
	if err := engine.SetTrustedProxies(proxies); err != nil {
		zap.L().Error("invalid trusted proxy configuration; proxy headers disabled", zap.Error(err))
		_ = engine.SetTrustedProxies(nil)
	}
}
