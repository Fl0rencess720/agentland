package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/agentd"
	"github.com/Fl0rencess720/agentland/pkg/common/logging"
	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func init() {
	logging.Init()
}

func main() {
	port := flag.String("port", "1883", "agentd HTTP server port")
	flag.Parse()

	bindEnv()
	config := &agentd.Config{
		Port:                 *port,
		WorkspaceRoot:        viper.GetString("agentd.workspace_root"),
		BuiltinSkillsDir:     viper.GetString("agentd.skills_dir"),
		MCPConfigPaths:       splitPaths(viper.GetString("agentd.mcp_config_paths")),
		Model:                viper.GetString("agentd.model"),
		ModelBaseURL:         viper.GetString("agentd.model_base_url"),
		ModelAPIKey:          viper.GetString("agentd.model_api_key"),
		SummaryModel:         viper.GetString("agentd.summary_model"),
		SystemPrompt:         viper.GetString("agentd.system_prompt"),
		ContextTokens:        viper.GetInt("agentd.context_tokens"),
		AuthEnabled:          viper.GetBool("agentd.auth_enabled"),
		SandboxJWTPublicPath: viper.GetString("sandbox.jwt.public_key_path"),
		SandboxJWTIssuer:     viper.GetString("sandbox.jwt.issuer"),
		SandboxJWTAudience:   viper.GetString("sandbox.jwt.audience"),
		SandboxJWTClockSkew:  viper.GetDuration("sandbox.jwt.clock_skew"),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer logging.Sync(zap.L())
	otelShutdown, err := observability.InitTracerProvider(ctx, observability.Config{
		Enabled:        viper.GetBool("otel.enabled"),
		ServiceName:    "agentland-agentd",
		ServiceVersion: "v1alpha1",
		Endpoint:       viper.GetString("otel.endpoint"),
		Insecure:       viper.GetBool("otel.insecure"),
		SampleRatio:    viper.GetFloat64("otel.sample_ratio"),
	})
	if err != nil {
		zap.L().Fatal("initialize tracing failed", zap.Error(err))
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if shutdownErr := otelShutdown(shutdownCtx); shutdownErr != nil {
			zap.L().Warn("shutdown tracer provider failed", zap.Error(shutdownErr))
		}
	}()
	server, err := agentd.NewServer(ctx, config)
	if err != nil {
		zap.L().Fatal("create agentd server failed", zap.Error(err))
	}
	defer server.Close()

	zap.L().Info("agentd server started", zap.String("port", config.Port), zap.String("workspace", config.WorkspaceRoot))
	if err := server.Serve(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		zap.L().Fatal("agentd server failed", zap.Error(err))
	}
}

func bindEnv() {
	viper.SetDefault("agentd.workspace_root", "/workspace")
	viper.SetDefault("agentd.skills_dir", "/app/skills")
	viper.SetDefault("agentd.mcp_config_paths", "/etc/agentland/mcp.json,/workspace/.agentland/mcp.json")
	viper.SetDefault("agentd.context_tokens", 128000)
	viper.SetDefault("agentd.auth_enabled", true)
	viper.SetDefault("sandbox.jwt.public_key_path", "/var/run/agentland/jwt/public.pem")
	viper.SetDefault("sandbox.jwt.issuer", "agentland-gateway")
	viper.SetDefault("sandbox.jwt.audience", "sandbox")
	viper.SetDefault("sandbox.jwt.clock_skew", "30s")
	viper.SetDefault("otel.enabled", false)
	viper.SetDefault("otel.endpoint", "otel-collector:4317")
	viper.SetDefault("otel.insecure", true)
	viper.SetDefault("otel.sample_ratio", 1.0)

	bindings := map[string]string{
		"agentd.workspace_root":       "AL_AGENTD_WORKSPACE_ROOT",
		"agentd.skills_dir":           "AL_AGENTD_SKILLS_DIR",
		"agentd.mcp_config_paths":     "AL_AGENTD_MCP_CONFIG_PATHS",
		"agentd.model":                "AL_AGENT_MODEL",
		"agentd.model_base_url":       "AL_AGENT_MODEL_BASE_URL",
		"agentd.model_api_key":        "AL_AGENT_MODEL_API_KEY",
		"agentd.summary_model":        "AL_AGENT_SUMMARY_MODEL",
		"agentd.system_prompt":        "AL_AGENTD_SYSTEM_PROMPT",
		"agentd.context_tokens":       "AL_AGENT_MODEL_CONTEXT_TOKENS",
		"agentd.auth_enabled":         "AL_AGENTD_AUTH_ENABLED",
		"sandbox.jwt.public_key_path": "AL_SANDBOX_JWT_PUBLIC_KEY_PATH",
		"sandbox.jwt.issuer":          "AL_SANDBOX_JWT_ISSUER",
		"sandbox.jwt.audience":        "AL_SANDBOX_JWT_AUDIENCE",
		"sandbox.jwt.clock_skew":      "AL_SANDBOX_JWT_CLOCK_SKEW",
		"otel.enabled":                "AL_OTEL_ENABLED",
		"otel.endpoint":               "AL_OTEL_EXPORTER_OTLP_ENDPOINT",
		"otel.insecure":               "AL_OTEL_EXPORTER_OTLP_INSECURE",
		"otel.sample_ratio":           "AL_OTEL_TRACES_SAMPLE_RATIO",
	}
	for key, env := range bindings {
		_ = viper.BindEnv(key, env)
	}
}

func splitPaths(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
