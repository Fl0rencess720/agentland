package agentd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChildProcessEnvKeepsOnlyExplicitRuntimeValues(t *testing.T) {
	t.Setenv("PATH", "/test/bin")
	t.Setenv("HOME", "/root")
	t.Setenv("AL_AGENT_MODEL_API_KEY", "model-secret")
	t.Setenv("OPENAI_API_KEY", "provider-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv(toolHomeEnv, "/home/agentd-tool")
	t.Setenv(toolUserEnv, "agentd-tool")

	env, err := childProcessEnv(nil)
	require.NoError(t, err)
	joined := strings.Join(env, "\n")
	require.Contains(t, joined, "PATH=/test/bin")
	require.Contains(t, joined, "HOME=/home/agentd-tool")
	require.Contains(t, joined, "USER=agentd-tool")
	require.NotContains(t, joined, "model-secret")
	require.NotContains(t, joined, "provider-secret")
	require.NotContains(t, joined, "github-secret")
}

func TestChildProcessEnvAllowsMCPOverridesButRejectsPlatformEnv(t *testing.T) {
	env, err := childProcessEnv(map[string]string{"MCP_TOKEN": "allowed"})
	require.NoError(t, err)
	require.Contains(t, env, "MCP_TOKEN=allowed")

	_, err = childProcessEnv(map[string]string{"AL_AGENT_MODEL_API_KEY": "blocked"})
	require.ErrorContains(t, err, "reserved")
}

func TestExpandMCPConfigEnvHidesAgentlandEnvironment(t *testing.T) {
	t.Setenv("AL_AGENT_MODEL_API_KEY", "model-secret")
	t.Setenv("MCP_TOKEN", "mcp-secret")

	expanded := expandMCPConfigEnv(`${AL_AGENT_MODEL_API_KEY}:${MCP_TOKEN}`)
	require.Equal(t, ":mcp-secret", expanded)
}

func TestChildProcessCredentialRequiresCompleteNonRootIdentity(t *testing.T) {
	t.Setenv(toolUIDEnv, "10001")
	t.Setenv(toolGIDEnv, "")
	_, err := childProcessSysProcAttr(false)
	require.ErrorContains(t, err, "configured together")

	t.Setenv(toolUIDEnv, "0")
	t.Setenv(toolGIDEnv, "10001")
	_, err = childProcessSysProcAttr(false)
	require.ErrorContains(t, err, "positive")
}

func TestMCPProcessUsesASeparateIdentityAndHome(t *testing.T) {
	t.Setenv(toolUIDEnv, "10001")
	t.Setenv(toolGIDEnv, "10001")
	t.Setenv(mcpUIDEnv, "10002")
	t.Setenv(mcpGIDEnv, "10002")
	t.Setenv(mcpHomeEnv, "/home/agentd-mcp")
	t.Setenv(mcpUserEnv, "agentd-mcp")

	toolUID, toolGID, configured, err := configuredProcessIdentity(toolIdentity)
	require.NoError(t, err)
	require.True(t, configured)
	mcpUID, mcpGID, configured, err := configuredProcessIdentity(mcpIdentity)
	require.NoError(t, err)
	require.True(t, configured)
	require.NotEqual(t, toolUID, mcpUID)
	require.Contains(t, processSupplementaryGroups(mcpIdentity, mcpGID), toolGID)

	env, err := processEnv(mcpIdentity, map[string]string{"MCP_TOKEN": "secret"})
	require.NoError(t, err)
	require.Contains(t, env, "HOME=/home/agentd-mcp")
	require.Contains(t, env, "USER=agentd-mcp")
	require.Contains(t, env, "MCP_TOKEN=secret")
}

func TestShellCannotReadSecretMCPEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("requires Linux root to switch child identities")
	}
	t.Setenv(toolUIDEnv, "61001")
	t.Setenv(toolGIDEnv, "61001")
	t.Setenv(toolHomeEnv, "/tmp")
	t.Setenv(toolUserEnv, "agentd-tool")
	t.Setenv(mcpUIDEnv, "61002")
	t.Setenv(mcpGIDEnv, "61002")
	t.Setenv(mcpHomeEnv, "/tmp")
	t.Setenv(mcpUserEnv, "agentd-mcp")

	env, err := processEnv(mcpIdentity, map[string]string{"MCP_SECRET": "hidden-value"})
	require.NoError(t, err)
	attr, err := processSysProcAttr(mcpIdentity, false)
	require.NoError(t, err)
	mcpProcess := exec.Command("sh", "-c", "exec sleep 30")
	mcpProcess.Env = env
	mcpProcess.SysProcAttr = attr
	require.NoError(t, mcpProcess.Start())
	t.Cleanup(func() {
		_ = mcpProcess.Process.Kill()
		_ = mcpProcess.Wait()
	})

	tools := &localTools{root: shellTestWorkspace(t)}
	result, err := tools.shell(context.Background(), shellInput{
		Command: fmt.Sprintf("cat /proc/%d/environ", mcpProcess.Process.Pid),
	})
	require.NoError(t, err)
	require.NotZero(t, result.ExitCode)
	require.NotContains(t, result.Output, "hidden-value")
}
