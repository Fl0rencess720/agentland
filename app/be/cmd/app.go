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
	Kafka             *data.KafkaPipeline
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
	kafka, err := data.NewKafkaPipeline(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize kafka pipeline: %w", err)
	}
	runEvents := kafka.EventStore()
	gateway := data.NewAgentlandGatewayClient()
	publisher, ok := gateway.(biz.PublicationGateway)
	if !ok {
		kafka.Close()
		return nil, fmt.Errorf("gateway does not support image publication")
	}
	projectUseCase := biz.NewProjectUsecaseWithPublishingAndTasks(projectRepo, runRepo, runEvents, gateway, publicationRepo, publisher, kafka, data.NewLangfuseScoreClient())
	preparationCoordinator := biz.NewPublicationPreparationCoordinator(publicationRepo, gateway, kafka)
	kafka.SetPublicationPreparationHandler(preparationCoordinator.Handle)

	httpServer := service.NewHTTPServer(
		middlewares.NewDefaultIPRateLimiter(),
		auth.NewAuthHandler(authUseCase),
		project.NewProjectHandler(projectUseCase),
		runRepo,
	)
	return &App{
		HTTPServer:        httpServer,
		RunWorker:         biz.NewRunWorkerWithEvents(runRepo, gateway, kafka.RunQueue(), kafka),
		PublicationWorker: biz.NewPublicationWorker(publicationRepo, publisher, kafka.PublicationQueue()),
		Kafka:             kafka,
	}, nil
}
