package handlers

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContextManagerExecuteShellSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	m := &contextManager{
		contexts: map[string]*kernelContext{},
	}

	kctx := newPersistentShellContextForTest(t, "ctx-shell-success", tmpDir)
	m.contexts[kctx.ID] = kctx

	t.Cleanup(func() {
		_ = m.removeContext(kctx.ID, true)
	})

	resp, err := m.execute(context.Background(), kctx.ID, "printf 'hello-shell\\n'", 1000)
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.ExitCode)
	require.Equal(t, "hello-shell\n", resp.Stdout)
	require.Equal(t, "", resp.Stderr)
	require.Equal(t, int64(1), resp.ExecutionCount)
}

func TestContextManagerExecuteShellStatePersists(t *testing.T) {
	tmpDir := t.TempDir()
	m := &contextManager{
		contexts: map[string]*kernelContext{},
	}

	kctx := newPersistentShellContextForTest(t, "ctx-shell-persist", tmpDir)
	m.contexts[kctx.ID] = kctx

	t.Cleanup(func() {
		_ = m.removeContext(kctx.ID, true)
	})

	_, err := m.execute(context.Background(), kctx.ID, "x=42", 1000)
	require.NoError(t, err)

	resp, err := m.execute(context.Background(), kctx.ID, "printf '%s\\n' \"$x\"", 1000)
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.ExitCode)
	require.Equal(t, "42\n", resp.Stdout)
	require.Equal(t, int64(2), resp.ExecutionCount)
}

func TestContextManagerExecuteShellNonZeroExitCode(t *testing.T) {
	tmpDir := t.TempDir()
	m := &contextManager{
		contexts: map[string]*kernelContext{},
	}

	kctx := newPersistentShellContextForTest(t, "ctx-shell-nonzero", tmpDir)
	m.contexts[kctx.ID] = kctx

	t.Cleanup(func() {
		_ = m.removeContext(kctx.ID, true)
	})

	resp, err := m.execute(context.Background(), kctx.ID, "echo shell-error 1>&2; false", 1000)
	require.NoError(t, err)
	require.Equal(t, int32(1), resp.ExitCode)
	require.Equal(t, "", resp.Stdout)
	require.Contains(t, resp.Stderr, "shell-error")
	require.Equal(t, int64(1), resp.ExecutionCount)
}

func newPersistentShellContextForTest(t *testing.T, id, dir string) *kernelContext {
	t.Helper()

	cmd := exec.Command("sh")
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stderr, err := cmd.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
		close(waitCh)
	}()
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	kctx := &kernelContext{
		ID:         id,
		Language:   contextLanguageShell,
		CWD:        dir,
		PID:        cmd.Process.Pid,
		RootDir:    dir,
		cmd:        cmd,
		waitCh:     waitCh,
		shellStdin: stdin,
		createdAt:  time.Now().UTC(),
	}
	kctx.lastActiveUnix.Store(time.Now().UnixNano())
	return kctx
}
