package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Fl0rencess720/agentland/pkg/common/logging"
	"github.com/Fl0rencess720/agentland/pkg/gateway"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"go.uber.org/zap"
)

func init() {
	logging.Init()
}

func main() {
	port := flag.String("port", "8080", "Gateway server port")
	flag.Parse()

	config := &config.Config{Port: *port}
	server, err := gateway.NewServer(config)
	if err != nil {
		zap.L().Fatal("New Server failed", zap.Error(err))
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer logging.Sync(zap.L())

	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(ctx); err != nil {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		zap.L().Info("Received shutdown signal, shutting down gracefully...")
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			zap.L().Error("Server shutdown error", zap.Error(err))
		}
		zap.L().Info("Server shutdown complete.")
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			zap.L().Info("Server shutdown complete.")
			return
		}
		zap.L().Fatal("Server error", zap.Error(err))
	}
}
