package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/Fl0rencess720/agentland/pkg/common/logging"
	"github.com/Fl0rencess720/agentland/pkg/korokd"
	"github.com/Fl0rencess720/agentland/pkg/korokd/config"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func init() {
	logging.Init()
}

func main() {
	port := flag.String("port", "1883", "korokd gRPC server port")
	flag.Parse()

	cfg := &config.Config{Port: *port}
	server, err := korokd.NewServer(cfg)
	if err != nil {
		zap.L().Fatal("New Server failed", zap.Error(err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer logging.Sync(zap.L())

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ctx)
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		zap.L().Info("Received shutdown signal, shutting down gracefully...")
		if err := <-errCh; err != nil && err != grpc.ErrServerStopped {
			zap.L().Error("Server shutdown error", zap.Error(err))
		}
		zap.L().Info("Server shutdown complete.")
	case err := <-errCh:
		if err == nil || err == grpc.ErrServerStopped {
			zap.L().Info("Server shutdown complete.")
			return
		}
		zap.L().Fatal("Server error", zap.Error(err))
	}
}
