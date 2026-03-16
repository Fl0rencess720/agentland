package configs

import (
	"strings"
	"time"

	"github.com/spf13/viper"
)

func Init() {
	viper.SetConfigType("yaml")
	viper.SetConfigName("config")
	viper.AddConfigPath("configs")

	viper.SetDefault("server.http.name", "agentland.app.be")
	viper.SetDefault("server.http.addr", ":18081")
	viper.SetDefault("redis.addr", "127.0.0.1:6379")
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.db", 0)
	viper.SetDefault("database.url", "")
	viper.SetDefault("agentland-gateway.url", "http://127.0.0.1:18080")
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
	viper.SetDefault("auth.user.default_plan", "free")

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	_ = viper.ReadInConfig()
}

func GetServiceName() string {
	return viper.GetString("server.http.name")
}
