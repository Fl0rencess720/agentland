package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Fl0rencess720/agentland/pkg/common/conf"
	"github.com/Fl0rencess720/agentland/pkg/common/logging"
	"github.com/Fl0rencess720/agentland/pkg/gateway"
	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/sandboxjwt"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func init() {
	logging.Init()
	conf.Init()
}

func main() {
	port := flag.String("port", "8080", "Gateway server port")
	flag.Parse()

	// 绑定环境变量
	viper.SetEnvPrefix("al")

	_ = viper.BindEnv("agentcore.address", "AL_AGENTCORE_ADDRESS")
	_ = viper.BindEnv("redis.addr", "AL_REDIS_ADDR")
	_ = viper.BindEnv("redis.password", "AL_REDIS_PASSWORD")
	_ = viper.BindEnv("redis.db", "AL_REDIS_DB")
	_ = viper.BindEnv("sandbox.jwt.private_key_path", "AL_SANDBOX_JWT_PRIVATE_KEY_PATH")
	_ = viper.BindEnv("sandbox.jwt.identity_secret_name", "AL_SANDBOX_JWT_IDENTITY_SECRET_NAME")
	_ = viper.BindEnv("sandbox.jwt.identity_secret_namespace", "AL_SANDBOX_JWT_IDENTITY_SECRET_NAMESPACE")
	_ = viper.BindEnv("sandbox.jwt.public_secret_name", "AL_SANDBOX_JWT_PUBLIC_SECRET_NAME")
	_ = viper.BindEnv("sandbox.jwt.public_secret_namespace", "AL_SANDBOX_JWT_PUBLIC_SECRET_NAMESPACE")
	_ = viper.BindEnv("sandbox.jwt.issuer", "AL_SANDBOX_JWT_ISSUER")
	_ = viper.BindEnv("sandbox.jwt.audience", "AL_SANDBOX_JWT_AUDIENCE")
	_ = viper.BindEnv("sandbox.jwt.ttl", "AL_SANDBOX_JWT_TTL")
	_ = viper.BindEnv("sandbox.jwt.kid", "AL_SANDBOX_JWT_KID")

	viper.SetDefault("agentcore.address", "agentland-agentcore:8082")
	viper.SetDefault("sandbox.jwt.private_key_path", "/tmp/agentland/jwt/private.pem")
	viper.SetDefault("sandbox.jwt.identity_secret_name", "gateway-sandbox-jwt-identity")
	viper.SetDefault("sandbox.jwt.public_secret_name", "gateway-sandbox-jwt-public-key")
	viper.SetDefault("sandbox.jwt.public_secret_namespace", "agentland-sandboxes")
	viper.SetDefault("sandbox.jwt.issuer", "agentland-gateway")
	viper.SetDefault("sandbox.jwt.audience", "sandbox")
	viper.SetDefault("sandbox.jwt.ttl", "5m")
	viper.SetDefault("sandbox.jwt.kid", "default")

	privateKeyPath, err := sandboxjwt.EnsureGatewaySigningKey(context.Background(), sandboxjwt.BootstrapConfig{
		IdentitySecretName:      viper.GetString("sandbox.jwt.identity_secret_name"),
		IdentitySecretNamespace: viper.GetString("sandbox.jwt.identity_secret_namespace"),
		PublicSecretName:        viper.GetString("sandbox.jwt.public_secret_name"),
		PublicSecretNamespace:   viper.GetString("sandbox.jwt.public_secret_namespace"),
		LocalPrivateKeyPath:     viper.GetString("sandbox.jwt.private_key_path"),
	})
	if err != nil {
		zap.L().Fatal("Ensure gateway sandbox JWT key failed", zap.Error(err))
		return
	}

	config := &config.Config{
		Port:                  *port,
		SandboxJWTPrivatePath: privateKeyPath,
		SandboxJWTIssuer:      viper.GetString("sandbox.jwt.issuer"),
		SandboxJWTAudience:    viper.GetString("sandbox.jwt.audience"),
		SandboxJWTTTL:         viper.GetDuration("sandbox.jwt.ttl"),
		SandboxJWTKID:         viper.GetString("sandbox.jwt.kid"),
	}

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
