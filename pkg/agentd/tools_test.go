package agentd

import (
	"context"
	"os"
	"path/filepath"
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

func TestLocalToolsWriteEditAndShell(t *testing.T) {
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	tools := &localTools{root: workspace}

	_, err = tools.writeFile(context.Background(), writeFileInput{Path: "src/app.txt", Content: "hello world"})
	require.NoError(t, err)
	_, err = tools.editFile(context.Background(), editFileInput{Path: "src/app.txt", OldText: "world", NewText: "agent"})
	require.NoError(t, err)
	content, err := tools.readFile(context.Background(), pathInput{Path: "src/app.txt"})
	require.NoError(t, err)
	require.Equal(t, "hello agent", content)

	result, err := tools.shell(context.Background(), shellInput{Command: "pwd; printf test", CWD: "src"})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.Contains(t, result.Output, filepath.Join(workspace, "src"))
	require.Contains(t, result.Output, "test")
}
