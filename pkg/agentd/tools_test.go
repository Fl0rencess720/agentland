package agentd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalToolsStayInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, "escape")))
	tools := &localTools{root: workspace}

	_, err := tools.readFile(context.Background(), pathInput{Path: "../secret.txt"})
	require.ErrorContains(t, err, "escapes workspace")
	_, err = tools.readFile(context.Background(), pathInput{Path: "escape/secret.txt"})
	require.ErrorContains(t, err, "escapes workspace")
	_, err = tools.writeFile(context.Background(), writeFileInput{Path: "escape/new.txt", Content: "bad"})
	require.ErrorContains(t, err, "escapes workspace")
}

func TestLocalToolsResistSymlinkSwapDuringRootFileOperations(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "inside"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "inside", "secret.txt"), []byte("inside"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600))
	require.NoError(t, os.Symlink("inside", filepath.Join(workspace, "switch")))
	tools := &localTools{root: workspace}

	done := make(chan struct{})
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		for index := 0; ; index++ {
			select {
			case <-done:
				return
			default:
			}
			target := "inside"
			if index%2 != 0 {
				target = outside
			}
			next := filepath.Join(workspace, fmt.Sprintf(".switch-%d", index%2))
			_ = os.Remove(next)
			if os.Symlink(target, next) == nil {
				_ = os.Rename(next, filepath.Join(workspace, "switch"))
			}
		}
	}()

	escapedRead := false
	for range 500 {
		content, readErr := tools.readFile(context.Background(), pathInput{Path: "switch/secret.txt"})
		if readErr == nil && content != "inside" {
			escapedRead = true
			break
		}
		_, _ = tools.writeFile(context.Background(), writeFileInput{Path: "switch/target.txt", Content: "workspace"})
	}
	close(done)
	<-swapped
	require.False(t, escapedRead)
	_, err = os.Stat(filepath.Join(outside, "target.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLocalToolsProtectAgentlandStateAndAllowShellLogs(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".agentland", "conversations"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".agentland", "logs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".agentland", "conversations", "history.jsonl"), []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".agentland", "logs", "shell.log"), []byte("log output"), 0o600))
	require.NoError(t, os.Symlink(".agentland/conversations/history.jsonl", filepath.Join(workspace, "history-link")))
	tools := &localTools{root: workspace}

	log, err := tools.readFile(context.Background(), pathInput{Path: ".agentland/logs/shell.log"})
	require.NoError(t, err)
	require.Equal(t, "log output", log)
	_, err = tools.readFile(context.Background(), pathInput{Path: ".agentland/conversations/history.jsonl"})
	require.ErrorIs(t, err, errWorkspaceInternal)
	_, err = tools.readFile(context.Background(), pathInput{Path: "history-link"})
	require.ErrorIs(t, err, errWorkspaceInternal)
	_, err = tools.writeFile(context.Background(), writeFileInput{Path: ".agentland/mcp.json", Content: "{}"})
	require.ErrorIs(t, err, errWorkspaceInternal)
	_, err = tools.editFile(context.Background(), editFileInput{Path: ".agentland/logs/shell.log", OldText: "log", NewText: "changed"})
	require.ErrorIs(t, err, errWorkspaceInternal)
	_, err = tools.listFiles(context.Background(), pathInput{Path: ".agentland"})
	require.ErrorIs(t, err, errWorkspaceInternal)
	_, err = tools.grep(context.Background(), grepInput{Path: ".agentland", Pattern: "secret"})
	require.ErrorIs(t, err, errWorkspaceInternal)
}

func TestLocalToolsWriteEditAndShell(t *testing.T) {
	workspace := shellTestWorkspace(t)
	tools := &localTools{root: workspace}

	_, err := tools.writeFile(context.Background(), writeFileInput{Path: "src/app.txt", Content: "hello world"})
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(workspace, "src", "app.txt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Join(workspace, "src"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), dirInfo.Mode().Perm())
	_, err = tools.editFile(context.Background(), editFileInput{Path: "src/app.txt", OldText: "world", NewText: "agent"})
	require.NoError(t, err)
	content, err := tools.readFile(context.Background(), pathInput{Path: "src/app.txt"})
	require.NoError(t, err)
	require.Equal(t, "hello agent", content)

	result, err := tools.shell(context.Background(), shellInput{
		Command: "pwd; cat app.txt; printf changed > app.txt",
		CWD:     "src",
	})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.Contains(t, result.Output, filepath.Join(workspace, "src"))
	require.Contains(t, result.Output, "hello agent")
	require.Empty(t, result.LogPath)
	content, err = tools.readFile(context.Background(), pathInput{Path: "src/app.txt"})
	require.NoError(t, err)
	require.Equal(t, "changed", content)
}

func TestShellDoesNotInheritPlatformSecrets(t *testing.T) {
	t.Setenv("AL_AGENT_MODEL_API_KEY", "model-secret")
	t.Setenv("OPENAI_API_KEY", "provider-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	workspace := shellTestWorkspace(t)
	tools := &localTools{root: workspace}

	result, err := tools.shell(context.Background(), shellInput{Command: "env"})
	require.NoError(t, err)
	require.NotContains(t, result.Output, "model-secret")
	require.NotContains(t, result.Output, "provider-secret")
	require.NotContains(t, result.Output, "github-secret")
}

func TestShellStoresOversizedOutputInWorkspaceLog(t *testing.T) {
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	tools := &localTools{root: workspace}

	const outputBytes = maxShellOutputBytes * 4
	result, err := tools.shell(context.Background(), shellInput{
		Command: fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x", outputBytes),
	})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.NotEmpty(t, result.LogPath)
	require.Contains(t, result.Output, "full log: "+result.LogPath)
	require.Less(t, len(result.Output), outputBytes)

	logPath := filepath.Join(workspace, filepath.FromSlash(result.LogPath))
	info, err := os.Stat(logPath)
	require.NoError(t, err)
	require.Equal(t, int64(outputBytes), info.Size())
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("x", outputBytes), string(data))
}

func TestReadFileBoundsLargeContent(t *testing.T) {
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	tools := &localTools{root: workspace}
	content := strings.Repeat("x", maxToolOutputBytes*2)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "large.txt"), []byte(content), 0o600))

	result, err := tools.readFile(context.Background(), pathInput{Path: "large.txt"})
	require.NoError(t, err)
	require.LessOrEqual(t, len(result), maxToolOutputBytes)
	require.Contains(t, result, fmt.Sprintf("original size: %d bytes", len(content)))
}

func TestEditFileRejectsOversizedInputAndResult(t *testing.T) {
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	tools := &localTools{root: workspace}

	largePath := filepath.Join(workspace, "large.txt")
	file, err := os.Create(largePath)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxEditableFileBytes+1))
	require.NoError(t, file.Close())
	_, err = tools.editFile(context.Background(), editFileInput{Path: "large.txt", OldText: "a", NewText: "b"})
	require.ErrorContains(t, err, "file is too large to edit")

	expanding := strings.Repeat("a", maxEditableFileBytes/2+1)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "expand.txt"), []byte(expanding), 0o600))
	_, err = tools.editFile(context.Background(), editFileInput{
		Path:       "expand.txt",
		OldText:    "a",
		NewText:    "aa",
		ReplaceAll: true,
	})
	require.ErrorContains(t, err, "edited file would exceed")
}

func TestListFilesStopsAtResultLimit(t *testing.T) {
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	tools := &localTools{root: workspace}
	for i := 0; i <= maxListFileResults; i++ {
		name := filepath.Join(workspace, fmt.Sprintf("file-%04d.txt", i))
		require.NoError(t, os.WriteFile(name, nil, 0o600))
	}

	_, err = tools.listFiles(context.Background(), pathInput{})
	require.ErrorContains(t, err, "file list exceeds")
}

func TestGrepStopsAtMatchAndLineLimits(t *testing.T) {
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	tools := &localTools{root: workspace}
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "matches.txt"),
		[]byte(strings.Repeat("match\n", maxGrepMatches+1)),
		0o600,
	))

	_, err = tools.grep(context.Background(), grepInput{Pattern: "match", Path: "matches.txt"})
	require.ErrorContains(t, err, "grep exceeds")

	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, "long.txt"),
		[]byte(strings.Repeat("x", maxGrepLineBytes+1)),
		0o600,
	))
	_, err = tools.grep(context.Background(), grepInput{Pattern: "x", Path: "long.txt"})
	require.ErrorContains(t, err, "scan long.txt")
}

func TestListAndGrepSkipSymlinks(t *testing.T) {
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, "linked.txt")))
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "node_modules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "node_modules", "dependency.js"), []byte("secret"), 0o600))
	tools := &localTools{root: workspace}

	files, err := tools.listFiles(context.Background(), pathInput{})
	require.NoError(t, err)
	require.Empty(t, files)
	matches, err := tools.grep(context.Background(), grepInput{Pattern: "secret"})
	require.NoError(t, err)
	require.Empty(t, matches)
}

func shellTestWorkspace(t *testing.T) string {
	t.Helper()
	workspace, err := os.MkdirTemp("", "agentd-shell-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(workspace)) })
	workspace, err = filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	return workspace
}
