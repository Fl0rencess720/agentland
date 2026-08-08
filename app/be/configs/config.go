package configs

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	DefaultPreviewPublicURLTemplate = "http://{token}.localhost:18081"
	previewTokenPlaceholder         = "{token}"
)

var previewTokenPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func Init() error {
	viper.SetConfigType("yaml")
	viper.SetConfigName("config")
	viper.AddConfigPath("configs")

	viper.SetDefault("server.http.name", "agentland.app.be")
	viper.SetDefault("server.http.addr", ":18081")
	viper.SetDefault("server.http.trusted_proxies", []string{})
	viper.SetDefault("server.http.cors.allowed_origins", []string{})
	viper.SetDefault("rate_limit.visitor_ttl", 15*time.Minute)
	viper.SetDefault("rate_limit.cleanup_interval", time.Minute)
	viper.SetDefault("rate_limit.preview.requests_per_second", 100)
	viper.SetDefault("rate_limit.preview.burst", 500)
	viper.SetDefault("redis.addr", "127.0.0.1:6379")
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.db", 0)
	viper.SetDefault("kafka.brokers", []string{"127.0.0.1:9092"})
	viper.SetDefault("kafka.client_id", "agentland-app-be")
	viper.SetDefault("kafka.run_topic", "agentland.app.run-tasks")
	viper.SetDefault("kafka.publication_topic", "agentland.app.publication-tasks")
	viper.SetDefault("kafka.event_topic", "agentland.app.run-events")
	viper.SetDefault("kafka.run_consumer_group", "agentland.app.run-workers")
	viper.SetDefault("kafka.publication_consumer_group", "agentland.app.publication-workers")
	viper.SetDefault("kafka.event_projector_group", "agentland.app.run-event-projectors")
	viper.SetDefault("kafka.task_partitions", 16)
	viper.SetDefault("kafka.event_partitions", 32)
	viper.SetDefault("kafka.replication_factor", 1)
	viper.SetDefault("kafka.event_retention", 7*24*time.Hour)
	viper.SetDefault("kafka.relay_poll_interval", 100*time.Millisecond)
	viper.SetDefault("kafka.relay_batch_size", 100)
	viper.SetDefault("kafka.outbox_retention", 7*24*time.Hour)
	viper.SetDefault("kafka.security_protocol", "plaintext")
	viper.SetDefault("kafka.sasl.mechanism", "plain")
	viper.SetDefault("kafka.sasl.username", "")
	viper.SetDefault("kafka.sasl.password", "")
	viper.SetDefault("kafka.tls.ca_file", "")
	viper.SetDefault("kafka.tls.cert_file", "")
	viper.SetDefault("kafka.tls.key_file", "")
	viper.SetDefault("kafka.tls.server_name", "")
	viper.SetDefault("database.url", "")
	viper.SetDefault("storage.s3.endpoint", "")
	viper.SetDefault("storage.s3.region", "us-east-1")
	viper.SetDefault("storage.s3.bucket", "")
	viper.SetDefault("storage.s3.access_key", "")
	viper.SetDefault("storage.s3.secret_key", "")
	viper.SetDefault("storage.s3.path_style", false)
	viper.SetDefault("storage.s3.key_prefix", "agentland")
	viper.SetDefault("storage.s3.max_snapshot_bytes", int64(8<<20))
	viper.SetDefault("agentland-gateway.url", "http://127.0.0.1:18080")
	viper.SetDefault("agentland-gateway.runtime.name", "default-runtime")
	viper.SetDefault("agentland-gateway.runtime.namespace", "agentland-sandboxes")
	viper.SetDefault("agentland-gateway.publisher_token", "")
	viper.SetDefault("preview.public_url_template", DefaultPreviewPublicURLTemplate)
	viper.SetDefault("runtime.idle_timeout", 15*time.Minute)
	viper.SetDefault("runtime.max_session_duration", time.Hour)
	viper.SetDefault("worker.heartbeat_interval", 5*time.Second)
	viper.SetDefault("worker.cancel_poll_interval", 250*time.Millisecond)
	viper.SetDefault("worker.orphan_timeout", 30*time.Second)
	viper.SetDefault("worker.parallelism", 4)
	viper.SetDefault("publication.worker.heartbeat_interval", 5*time.Second)
	viper.SetDefault("publication.worker.cancel_poll_interval", 250*time.Millisecond)
	viper.SetDefault("publication.worker.orphan_timeout", 30*time.Second)
	viper.SetDefault("publication.worker.parallelism", 2)
	viper.SetDefault("otel.enabled", false)
	viper.SetDefault("otel.endpoint", "otel-collector:4317")
	viper.SetDefault("otel.insecure", true)
	viper.SetDefault("otel.sample_ratio", 0.1)
	viper.SetDefault("langfuse.enabled", false)
	viper.SetDefault("langfuse.base_url", "https://cloud.langfuse.com")
	viper.SetDefault("langfuse.public_key", "")
	viper.SetDefault("langfuse.secret_key", "")
	viper.SetDefault("auth.github.client_id", "")
	viper.SetDefault("auth.github.client_secret", "")
	viper.SetDefault("auth.github.redirect_uri_allowlist", []string{})
	viper.SetDefault("auth.github.scopes", []string{"read:user", "user:email"})
	viper.SetDefault("auth.github.authorize_url", "https://github.com/login/oauth/authorize")
	viper.SetDefault("auth.github.token_url", "https://github.com/login/oauth/access_token")
	viper.SetDefault("auth.github.api_base_url", "https://api.github.com")
	viper.SetDefault("auth.jwt.issuer", "agentland-app-be")
	viper.SetDefault("auth.jwt.audience", "agentland-app")
	viper.SetDefault("auth.jwt.private_key_path", "")
	viper.SetDefault("auth.jwt.public_key_path", "")
	viper.SetDefault("auth.access_ttl", 15*time.Minute)
	viper.SetDefault("auth.refresh_ttl", 30*24*time.Hour)
	viper.SetDefault("auth.oauth_state_ttl", 10*time.Minute)
	viper.SetDefault("auth.oauth_cookie_secure", false)
	viper.SetDefault("auth.user.default_plan", "free")

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	_ = viper.ReadInConfig()

	if err := ValidatePreviewPublicURLTemplate(viper.GetString("preview.public_url_template")); err != nil {
		return err
	}
	if err := validateKafka(); err != nil {
		return err
	}
	if err := validateWorkerLease("worker", viper.GetDuration("worker.heartbeat_interval"), viper.GetDuration("worker.orphan_timeout")); err != nil {
		return err
	}
	return validateWorkerLease("publication.worker", viper.GetDuration("publication.worker.heartbeat_interval"), viper.GetDuration("publication.worker.orphan_timeout"))
}

func validateKafka() error {
	if len(viper.GetStringSlice("kafka.brokers")) == 0 {
		return fmt.Errorf("kafka.brokers is required")
	}
	for _, key := range []string{"run_topic", "publication_topic", "event_topic", "run_consumer_group", "publication_consumer_group", "event_projector_group"} {
		if strings.TrimSpace(viper.GetString("kafka."+key)) == "" {
			return fmt.Errorf("kafka.%s is required", key)
		}
	}
	if viper.GetInt("kafka.task_partitions") <= 0 || viper.GetInt("kafka.event_partitions") <= 0 || viper.GetInt("kafka.replication_factor") <= 0 {
		return fmt.Errorf("kafka partition and replication settings must be positive")
	}
	if viper.GetDuration("kafka.event_retention") < 24*time.Hour {
		return fmt.Errorf("kafka.event_retention must be at least 24h")
	}
	if viper.GetDuration("kafka.relay_poll_interval") <= 0 || viper.GetInt("kafka.relay_batch_size") <= 0 {
		return fmt.Errorf("kafka relay settings must be positive")
	}
	return nil
}

func GetServiceName() string {
	return viper.GetString("server.http.name")
}

func validateWorkerLease(name string, heartbeat, ttl time.Duration) error {
	if heartbeat <= 0 {
		return fmt.Errorf("%s.heartbeat_interval must be positive", name)
	}
	if ttl < 3*heartbeat {
		return fmt.Errorf("%s.orphan_timeout must be at least three times heartbeat_interval", name)
	}
	return nil
}

func ValidatePreviewPublicURLTemplate(raw string) error {
	template := strings.TrimSpace(raw)
	if !strings.Contains(template, previewTokenPlaceholder) {
		return fmt.Errorf("preview.public_url_template must contain %s", previewTokenPlaceholder)
	}

	const sampleToken = "agentland-preview-token"
	parsed, err := url.Parse(strings.ReplaceAll(template, previewTokenPlaceholder, sampleToken))
	if err != nil {
		return fmt.Errorf("parse preview.public_url_template: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("preview.public_url_template must be an absolute HTTP(S) origin")
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("preview.public_url_template must not contain credentials, a path, query, or fragment")
	}
	if !strings.Contains(parsed.Hostname(), sampleToken) {
		return fmt.Errorf("preview.public_url_template must place %s in the hostname", previewTokenPlaceholder)
	}
	if !validPreviewHostname(parsed.Hostname()) {
		return fmt.Errorf("preview.public_url_template contains an invalid hostname")
	}
	return nil
}

func PreviewPublicURL(rawTemplate, token string) (string, error) {
	if err := ValidatePreviewPublicURLTemplate(rawTemplate); err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if !previewTokenPattern.MatchString(token) {
		return "", fmt.Errorf("preview token contains unsafe hostname characters")
	}

	parsed, err := url.Parse(strings.ReplaceAll(strings.TrimSpace(rawTemplate), previewTokenPlaceholder, token))
	if err != nil || !validPreviewHostname(parsed.Hostname()) {
		return "", fmt.Errorf("build preview public URL")
	}
	parsed.Path = "/p/" + token + "/"
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validPreviewHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if !previewTokenPattern.MatchString(label) {
			return false
		}
	}
	return true
}
