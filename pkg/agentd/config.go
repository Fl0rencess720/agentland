package agentd

import "time"

const DefaultSystemPrompt = `You are an autonomous coding agent running inside an isolated workspace.
Discuss requirements, answer questions, plan, and clarify without modifying the workspace. You may use read-only tools when they help you answer.
When the user clearly asks you to implement, modify, or execute something, work directly in /workspace: inspect the existing files, make the change with tools, run relevant checks, and report concise results.`

type Config struct {
	Port string

	WorkspaceRoot    string
	BuiltinSkillsDir string
	MCPConfigPaths   []string

	Model         string
	ModelBaseURL  string
	ModelAPIKey   string
	SummaryModel  string
	SystemPrompt  string
	ContextTokens int

	AuthEnabled          bool
	SandboxJWTPublicPath string
	SandboxJWTIssuer     string
	SandboxJWTAudience   string
	SandboxJWTClockSkew  time.Duration
}
