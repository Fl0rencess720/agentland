package config

import "time"

type Config struct {
	Port string `json:"port"`

	SandboxJWTPrivatePath string        `json:"sandbox_jwt_private_path"`
	SandboxJWTIssuer      string        `json:"sandbox_jwt_issuer"`
	SandboxJWTAudience    string        `json:"sandbox_jwt_audience"`
	SandboxJWTTTL         time.Duration `json:"sandbox_jwt_ttl"`
	SandboxJWTKID         string        `json:"sandbox_jwt_kid"`

	DefaultAgentRuntimeName      string `json:"default_agent_runtime_name"`
	DefaultAgentRuntimeNamespace string `json:"default_agent_runtime_namespace"`

	PublisherEnabled         bool          `json:"publisher_enabled"`
	BuildctlPath             string        `json:"buildctl_path"`
	BuildKitAddress          string        `json:"buildkit_address"`
	BuildKitPlatform         string        `json:"buildkit_platform"`
	BuildKitTimeout          time.Duration `json:"buildkit_timeout"`
	BuildKitCACert           string        `json:"buildkit_ca_cert"`
	BuildKitClientCert       string        `json:"buildkit_client_cert"`
	BuildKitClientKey        string        `json:"buildkit_client_key"`
	RegistryRepositoryPrefix string        `json:"registry_repository_prefix"`
	RegistryDockerConfig     string        `json:"registry_docker_config"`
	BuildKitAllowInsecure    bool          `json:"buildkit_allow_insecure"`
	PublisherServiceToken    string        `json:"publisher_service_token"`
}
