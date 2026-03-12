package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/configs"
	"go.uber.org/zap"
)

func init() {
	flag.Parse()
	configs.Init()
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	zap.ReplaceGlobals(logger)
}

func main() {
	app := wireApp()

	go func() {
		if err := app.HTTPServer.Server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zap.L().Error("HTTP Server ListenAndServe", zap.Error(err))
			panic(err)
		}
	}()

	closeServers(app.HTTPServer.Server)
}

func closeServers(servers ...*http.Server) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
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
