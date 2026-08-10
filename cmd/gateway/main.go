package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/conf"
	"github.com/Fl0rencess720/agentland/pkg/common/logging"
	"github.com/Fl0rencess720/agentland/pkg/common/observability"
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
	_ = viper.BindEnv("agent_runtime.default_name", "AL_AGENT_RUNTIME_DEFAULT_NAME")
	_ = viper.BindEnv("agent_runtime.default_namespace", "AL_AGENT_RUNTIME_DEFAULT_NAMESPACE")
	_ = viper.BindEnv("publisher.enabled", "AL_PUBLISHER_ENABLED")
	_ = viper.BindEnv("publisher.buildctl_path", "AL_BUILDCTL_PATH")
	_ = viper.BindEnv("publisher.buildkit_address", "AL_BUILDKIT_ADDRESS")
	_ = viper.BindEnv("publisher.platform", "AL_BUILDKIT_PLATFORM")
	_ = viper.BindEnv("publisher.timeout", "AL_BUILDKIT_TIMEOUT")
	_ = viper.BindEnv("publisher.buildkit_ca_cert", "AL_BUILDKIT_CA_CERT")
	_ = viper.BindEnv("publisher.buildkit_client_cert", "AL_BUILDKIT_CLIENT_CERT")
	_ = viper.BindEnv("publisher.buildkit_client_key", "AL_BUILDKIT_CLIENT_KEY")
	_ = viper.BindEnv("publisher.repository_prefix", "AL_REGISTRY_REPOSITORY_PREFIX")
	_ = viper.BindEnv("publisher.docker_config", "AL_REGISTRY_DOCKER_CONFIG")
	_ = viper.BindEnv("publisher.buildkit_allow_insecure", "AL_BUILDKIT_ALLOW_INSECURE")
	_ = viper.BindEnv("publisher.service_token", "AL_PUBLISHER_SERVICE_TOKEN")
	_ = viper.BindEnv("applications.namespace", "AL_APPLICATION_NAMESPACE")
	_ = viper.BindEnv("applications.base_domain", "AL_APPLICATION_BASE_DOMAIN")
	_ = viper.BindEnv("applications.ingress_class", "AL_APPLICATION_INGRESS_CLASS")
	_ = viper.BindEnv("applications.tls_secret", "AL_APPLICATION_TLS_SECRET")
	_ = viper.BindEnv("applications.runtime_class", "AL_APPLICATION_RUNTIME_CLASS")
	_ = viper.BindEnv("applications.image_pull_secret", "AL_APPLICATION_IMAGE_PULL_SECRET")
	_ = viper.BindEnv("applications.port", "AL_APPLICATION_PORT")
	_ = viper.BindEnv("applications.replicas", "AL_APPLICATION_REPLICAS")
	_ = viper.BindEnv("applications.deploy_timeout", "AL_APPLICATION_DEPLOY_TIMEOUT")
	_ = viper.BindEnv("applications.cpu_request", "AL_APPLICATION_CPU_REQUEST")
	_ = viper.BindEnv("applications.memory_request", "AL_APPLICATION_MEMORY_REQUEST")
	_ = viper.BindEnv("applications.cpu_limit", "AL_APPLICATION_CPU_LIMIT")
	_ = viper.BindEnv("applications.memory_limit", "AL_APPLICATION_MEMORY_LIMIT")
	_ = viper.BindEnv("otel.enabled", "AL_OTEL_ENABLED")
	_ = viper.BindEnv("otel.endpoint", "AL_OTEL_EXPORTER_OTLP_ENDPOINT")
	_ = viper.BindEnv("otel.insecure", "AL_OTEL_EXPORTER_OTLP_INSECURE")
	_ = viper.BindEnv("otel.sample_ratio", "AL_OTEL_TRACES_SAMPLE_RATIO")

	viper.SetDefault("agentcore.address", "agentland-agentcore:8082")
	viper.SetDefault("sandbox.jwt.private_key_path", "/tmp/agentland/jwt/private.pem")
	viper.SetDefault("sandbox.jwt.identity_secret_name", "gateway-sandbox-jwt-identity")
	viper.SetDefault("sandbox.jwt.public_secret_name", "gateway-sandbox-jwt-public-key")
	viper.SetDefault("sandbox.jwt.public_secret_namespace", "agentland-sandboxes")
	viper.SetDefault("sandbox.jwt.issuer", "agentland-gateway")
	viper.SetDefault("sandbox.jwt.audience", "sandbox")
	viper.SetDefault("sandbox.jwt.ttl", "5m")
	viper.SetDefault("sandbox.jwt.kid", "default")
	viper.SetDefault("agent_runtime.default_name", "default-runtime")
	viper.SetDefault("agent_runtime.default_namespace", "agentland-sandboxes")
	viper.SetDefault("publisher.enabled", false)
	viper.SetDefault("publisher.buildctl_path", "buildctl")
	viper.SetDefault("publisher.platform", "linux/amd64")
	viper.SetDefault("publisher.timeout", "20m")
	viper.SetDefault("publisher.buildkit_allow_insecure", false)
	viper.SetDefault("applications.namespace", "agentland-apps")
	viper.SetDefault("applications.port", 8080)
	viper.SetDefault("applications.replicas", 1)
	viper.SetDefault("applications.deploy_timeout", "5m")
	viper.SetDefault("applications.cpu_request", "50m")
	viper.SetDefault("applications.memory_request", "64Mi")
	viper.SetDefault("applications.cpu_limit", "500m")
	viper.SetDefault("applications.memory_limit", "512Mi")
	viper.SetDefault("otel.enabled", false)
	viper.SetDefault("otel.endpoint", "otel-collector:4317")
	viper.SetDefault("otel.insecure", true)
	viper.SetDefault("otel.sample_ratio", 0.1)

	otelShutdown, err := observability.InitTracerProvider(context.Background(), observability.Config{
		Enabled:        viper.GetBool("otel.enabled"),
		ServiceName:    "agentland-gateway",
		ServiceVersion: "v1alpha1",
		Endpoint:       viper.GetString("otel.endpoint"),
		Insecure:       viper.GetBool("otel.insecure"),
		SampleRatio:    viper.GetFloat64("otel.sample_ratio"),
	})
	if err != nil {
		zap.L().Fatal("Initialize tracing failed", zap.Error(err))
		return
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := otelShutdown(shutdownCtx); shutdownErr != nil {
			zap.L().Warn("Shutdown tracer provider failed", zap.Error(shutdownErr))
		}
	}()

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
		Port:                         *port,
		SandboxJWTPrivatePath:        privateKeyPath,
		SandboxJWTIssuer:             viper.GetString("sandbox.jwt.issuer"),
		SandboxJWTAudience:           viper.GetString("sandbox.jwt.audience"),
		SandboxJWTTTL:                viper.GetDuration("sandbox.jwt.ttl"),
		SandboxJWTKID:                viper.GetString("sandbox.jwt.kid"),
		DefaultAgentRuntimeName:      viper.GetString("agent_runtime.default_name"),
		DefaultAgentRuntimeNamespace: viper.GetString("agent_runtime.default_namespace"),
		PublisherEnabled:             viper.GetBool("publisher.enabled"),
		BuildctlPath:                 viper.GetString("publisher.buildctl_path"),
		BuildKitAddress:              viper.GetString("publisher.buildkit_address"),
		BuildKitPlatform:             viper.GetString("publisher.platform"),
		BuildKitTimeout:              viper.GetDuration("publisher.timeout"),
		BuildKitCACert:               viper.GetString("publisher.buildkit_ca_cert"),
		BuildKitClientCert:           viper.GetString("publisher.buildkit_client_cert"),
		BuildKitClientKey:            viper.GetString("publisher.buildkit_client_key"),
		RegistryRepositoryPrefix:     viper.GetString("publisher.repository_prefix"),
		RegistryDockerConfig:         viper.GetString("publisher.docker_config"),
		BuildKitAllowInsecure:        viper.GetBool("publisher.buildkit_allow_insecure"),
		PublisherServiceToken:        viper.GetString("publisher.service_token"),
		ApplicationNamespace:         viper.GetString("applications.namespace"),
		ApplicationBaseDomain:        viper.GetString("applications.base_domain"),
		ApplicationIngressClass:      viper.GetString("applications.ingress_class"),
		ApplicationTLSSecret:         viper.GetString("applications.tls_secret"),
		ApplicationRuntimeClass:      viper.GetString("applications.runtime_class"),
		ApplicationImagePullSecret:   viper.GetString("applications.image_pull_secret"),
		ApplicationPort:              int32(viper.GetInt("applications.port")),
		ApplicationReplicas:          int32(viper.GetInt("applications.replicas")),
		ApplicationDeployTimeout:     viper.GetDuration("applications.deploy_timeout"),
		ApplicationCPURequest:        viper.GetString("applications.cpu_request"),
		ApplicationMemoryRequest:     viper.GetString("applications.memory_request"),
		ApplicationCPULimit:          viper.GetString("applications.cpu_limit"),
		ApplicationMemoryLimit:       viper.GetString("applications.memory_limit"),
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
