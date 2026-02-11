package config

import "time"

type Config struct {
	Port string `json:"port"`

	SandboxJWTPrivatePath string        `json:"sandbox_jwt_private_path"`
	SandboxJWTIssuer      string        `json:"sandbox_jwt_issuer"`
	SandboxJWTAudience    string        `json:"sandbox_jwt_audience"`
	SandboxJWTTTL         time.Duration `json:"sandbox_jwt_ttl"`
	SandboxJWTKID         string        `json:"sandbox_jwt_kid"`
}
