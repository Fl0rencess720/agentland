package config

import "time"

type Config struct {
	Port string `json:"port"`

	SandboxJWTPublicPath string        `json:"sandbox_jwt_public_path"`
	SandboxJWTIssuer     string        `json:"sandbox_jwt_issuer"`
	SandboxJWTAudience   string        `json:"sandbox_jwt_audience"`
	SandboxJWTClockSkew  time.Duration `json:"sandbox_jwt_clock_skew"`
}
