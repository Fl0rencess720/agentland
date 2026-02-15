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
	"github.com/Fl0rencess720/agentland/pkg/harud"
	"github.com/Fl0rencess720/agentland/pkg/harud/config"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func init() {
	logging.Init()
}

func main() {
	port := flag.String("port", "1885", "harud HTTP server port")
	flag.Parse()

	viper.SetEnvPrefix("al")
	_ = viper.BindEnv("sandbox.jwt.public_key_path", "AL_SANDBOX_JWT_PUBLIC_KEY_PATH")
	_ = viper.BindEnv("sandbox.jwt.issuer", "AL_SANDBOX_JWT_ISSUER")
	_ = viper.BindEnv("sandbox.jwt.audience", "AL_SANDBOX_JWT_AUDIENCE")
	_ = viper.BindEnv("sandbox.jwt.clock_skew", "AL_SANDBOX_JWT_CLOCK_SKEW")
	_ = viper.BindEnv("harud.workspace_root", "AL_HARUD_WORKSPACE_ROOT")
	_ = viper.BindEnv("harud.max_file_bytes", "AL_HARUD_MAX_FILE_BYTES")

	viper.SetDefault("sandbox.jwt.public_key_path", "/var/run/agentland/jwt/public.pem")
	viper.SetDefault("sandbox.jwt.issuer", "agentland-gateway")
	viper.SetDefault("sandbox.jwt.audience", "sandbox")
	viper.SetDefault("sandbox.jwt.clock_skew", "30s")
	viper.SetDefault("harud.workspace_root", "/workspace")
	viper.SetDefault("harud.max_file_bytes", 1048576)

	cfg := &config.Config{
		Port:                 *port,
		SandboxJWTPublicPath: viper.GetString("sandbox.jwt.public_key_path"),
		SandboxJWTIssuer:     viper.GetString("sandbox.jwt.issuer"),
		SandboxJWTAudience:   viper.GetString("sandbox.jwt.audience"),
		SandboxJWTClockSkew:  viper.GetDuration("sandbox.jwt.clock_skew"),
		WorkspaceRoot:        viper.GetString("harud.workspace_root"),
		MaxFileBytes:         viper.GetInt64("harud.max_file_bytes"),
	}

	server, err := harud.NewServer(cfg)
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
