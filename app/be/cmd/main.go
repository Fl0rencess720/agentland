package main

import (
	"context"
	"flag"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/configs"
	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func init() {
	flag.Parse()
	if err := configs.Init(); err != nil {
		panic(err)
	}
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	zap.ReplaceGlobals(logger)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sampleRatio := viper.GetFloat64("otel.sample_ratio")
	tracingEnabled := viper.GetBool("otel.enabled") || viper.GetBool("langfuse.enabled")
	if viper.GetBool("langfuse.enabled") {
		sampleRatio = 1
	}
	otelShutdown, err := observability.InitTracerProvider(ctx, observability.Config{
		Enabled:        tracingEnabled,
		ServiceName:    configs.GetServiceName(),
		ServiceVersion: "v1alpha1",
		Endpoint:       viper.GetString("otel.endpoint"),
		Insecure:       viper.GetBool("otel.insecure"),
		SampleRatio:    sampleRatio,
	})
	if err != nil {
		zap.L().Fatal("initialize tracing failed", zap.Error(err))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := otelShutdown(shutdownCtx); shutdownErr != nil {
			zap.L().Warn("shutdown tracer provider failed", zap.Error(shutdownErr))
		}
	}()

	startupCtx, startupCancel := context.WithTimeout(ctx, 30*time.Second)
	app, err := newApp(startupCtx)
	startupCancel()
	if err != nil {
		zap.L().Fatal("initialize application failed", zap.Error(err))
	}

	var workerWG sync.WaitGroup
	workerWG.Add(5)
	go func() {
		defer workerWG.Done()
		app.RunWorker.Run(ctx)
	}()
	go func() {
		defer workerWG.Done()
		app.PublicationWorker.Run(ctx)
	}()
	go func() {
		defer workerWG.Done()
		app.Kafka.RunRelay(ctx)
	}()
	go func() {
		defer workerWG.Done()
		app.Kafka.RunEventProjector(ctx)
	}()
	go func() {
		defer workerWG.Done()
		app.Kafka.RunEventNotifier(ctx)
	}()

	serverErr := make(chan error, 1)
	go func() {
		err := app.HTTPServer.Server.ListenAndServe()
		if err == http.ErrServerClosed {
			err = nil
		}
		serverErr <- err
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if err != nil {
			zap.L().Error("HTTP server stopped", zap.Error(err))
		}
		stop()
	}
	closeServers(app.HTTPServer.Server)
	workerWG.Wait()
	app.Kafka.Close()
}

func closeServers(servers ...*http.Server) {
	zap.L().Info("Shutdown Servers ...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			zap.L().Error("Server forced to shutdown", zap.Error(err))
			continue
		}
		zap.L().Info("Server shutdown successfully", zap.String("addr", srv.Addr))
	}

	zap.L().Info("All servers exiting")
}
