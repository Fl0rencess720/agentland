package main

import (
	"context"
	"fmt"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/data"
	"github.com/Fl0rencess720/agentland/app/be/internal/service"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/auth"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/middlewares"
	"github.com/Fl0rencess720/agentland/app/be/internal/service/project"
)

type App struct {
	HTTPServer *service.HTTPServer
	RunWorker  *biz.RunWorker
}

func newApp(ctx context.Context) (*App, error) {
	if err := data.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("bootstrap application data: %w", err)
	}

	userRepo := data.NewUserRepo()
	authRepo := data.NewAuthRepo()
	oauthStateStore := data.NewOAuthStateStore()
	githubOAuthClient := data.NewGitHubOAuthClient()
	authUseCase := biz.NewAuthUsecase(userRepo, authRepo, oauthStateStore, githubOAuthClient)

	projectRepo := data.NewProjectRepo()
	runRepo := data.NewRunRepo()
	runEvents := data.NewRunEventStore()
	gateway := data.NewAgentlandGatewayClient()
	projectUseCase := biz.NewProjectUsecase(projectRepo, runRepo, runEvents, gateway)

	httpServer := service.NewHTTPServer(
		middlewares.NewDefaultIPRateLimiter(),
		auth.NewAuthHandler(authUseCase),
		project.NewProjectHandler(projectUseCase),
		runRepo,
	)
	return &App{
		HTTPServer: httpServer,
		RunWorker:  biz.NewRunWorker(runRepo, runEvents, gateway),
	}, nil
}
