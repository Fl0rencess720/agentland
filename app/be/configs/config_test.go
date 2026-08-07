package configs

import (
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestGatewayEnvironmentKeyUsesUnderscores(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("AGENTLAND_GATEWAY_URL", "http://gateway.example")
	require.NoError(t, Init())
	require.Equal(t, "http://gateway.example", viper.GetString("agentland-gateway.url"))
}

func TestCORSAllowedOriginsEnvironmentKey(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("SERVER_HTTP_CORS_ALLOWED_ORIGINS", "https://app.example.com,http://localhost:3000")
	require.NoError(t, Init())
	require.Equal(t, "https://app.example.com,http://localhost:3000", viper.GetString("server.http.cors.allowed_origins"))
}

func TestPreviewRateLimitEnvironmentKeys(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("RATE_LIMIT_PREVIEW_REQUESTS_PER_SECOND", "175.5")
	t.Setenv("RATE_LIMIT_PREVIEW_BURST", "800")
	require.NoError(t, Init())
	require.Equal(t, 175.5, viper.GetFloat64("rate_limit.preview.requests_per_second"))
	require.Equal(t, 800, viper.GetInt("rate_limit.preview.burst"))
}

func TestRuntimeMaxSessionDurationEnvironmentKey(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("RUNTIME_MAX_SESSION_DURATION", "45m")
	require.NoError(t, Init())
	require.Equal(t, 45*time.Minute, viper.GetDuration("runtime.max_session_duration"))
}

func TestRuntimeMaxSessionDurationDefaultsToOneHour(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("RUNTIME_MAX_SESSION_DURATION", "")
	require.NoError(t, Init())
	require.Equal(t, time.Hour, viper.GetDuration("runtime.max_session_duration"))
}

func TestLangfuseEnvironmentKeys(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("LANGFUSE_ENABLED", "true")
	t.Setenv("LANGFUSE_BASE_URL", "https://langfuse.example")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-test")
	require.NoError(t, Init())
	require.True(t, viper.GetBool("langfuse.enabled"))
	require.Equal(t, "https://langfuse.example", viper.GetString("langfuse.base_url"))
	require.Equal(t, "pk-test", viper.GetString("langfuse.public_key"))
	require.Equal(t, "sk-test", viper.GetString("langfuse.secret_key"))
}

func TestPreviewPublicURLTemplate(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("PREVIEW_PUBLIC_URL_TEMPLATE", DefaultPreviewPublicURLTemplate)
	require.NoError(t, Init())
	require.Equal(t, DefaultPreviewPublicURLTemplate, viper.GetString("preview.public_url_template"))

	previewURL, err := PreviewPublicURL(viper.GetString("preview.public_url_template"), "7b98d226-3f04-4c62-9365-dffc02a44e07")
	require.NoError(t, err)
	require.Equal(t, "http://7b98d226-3f04-4c62-9365-dffc02a44e07.localhost:18081/p/7b98d226-3f04-4c62-9365-dffc02a44e07/", previewURL)
}

func TestPreviewPublicURLTemplateEnvironmentKey(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("PREVIEW_PUBLIC_URL_TEMPLATE", "https://{token}.preview.example.com")
	require.NoError(t, Init())
	require.Equal(t, "https://{token}.preview.example.com", viper.GetString("preview.public_url_template"))
}

func TestInitRejectsInvalidPreviewPublicURLTemplate(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("PREVIEW_PUBLIC_URL_TEMPLATE", "https://preview.example.com")
	require.ErrorContains(t, Init(), "must contain {token}")
}

func TestPreviewPublicURLTemplateValidation(t *testing.T) {
	for _, value := range []string{
		"http://preview.localhost:18081",
		"ftp://{token}.preview.example.com",
		"https://preview.example.com/{token}",
	} {
		require.Error(t, ValidatePreviewPublicURLTemplate(value), value)
	}
	require.Error(t, func() error {
		_, err := PreviewPublicURL("https://{token}.preview.example.com", "unsafe_token")
		return err
	}())
}
