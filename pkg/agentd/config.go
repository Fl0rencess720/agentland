package agentd

import "time"

const DefaultSystemPrompt = `You are an autonomous coding agent running inside an isolated workspace.
Work directly in /workspace. Inspect the existing files before changing them, use tools to implement the request, run relevant checks, and keep the user informed with concise results.`

type Config struct {
	Port string

	WorkspaceRoot    string
	BuiltinSkillsDir string
	MCPConfigPaths   []string

	Model         string
	ModelBaseURL  string
	ModelAPIKey   string
	SystemPrompt  string
	ContextTokens int

	AuthEnabled          bool
	SandboxJWTPublicPath string
	SandboxJWTIssuer     string
	SandboxJWTAudience   string
	SandboxJWTClockSkew  time.Duration
}
