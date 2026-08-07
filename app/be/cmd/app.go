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
	HTTPServer        *service.HTTPServer
	RunWorker         *biz.RunWorker
	PublicationWorker *biz.PublicationWorker
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
	publicationRepo := data.NewPublicationRepo()
	runEvents := data.NewRunEventStore()
	gateway := data.NewAgentlandGatewayClient()
	publisher, ok := gateway.(biz.PublicationGateway)
	if !ok {
		return nil, fmt.Errorf("gateway does not support image publication")
	}
	projectUseCase := biz.NewProjectUsecaseWithPublishing(projectRepo, runRepo, runEvents, gateway, publicationRepo, publisher, data.NewLangfuseScoreClient())

	httpServer := service.NewHTTPServer(
		middlewares.NewDefaultIPRateLimiter(),
		auth.NewAuthHandler(authUseCase),
		project.NewProjectHandler(projectUseCase),
		runRepo,
	)
	return &App{
		HTTPServer:        httpServer,
		RunWorker:         biz.NewRunWorker(runRepo, runEvents, gateway),
		PublicationWorker: biz.NewPublicationWorker(publicationRepo, publisher),
	}, nil
}
