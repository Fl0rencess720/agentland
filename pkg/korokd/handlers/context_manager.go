package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/Fl0rencess720/agentland/pkg/korokd/pkgs/jupyter"
	"github.com/google/uuid"
)

const (
	// 所有 context 的工作目录必须位于该根目录下，避免访问容器内任意路径
	contextWorkspaceRoot  = "/workspace"
	contextLanguagePython = "python"
	contextLanguageShell  = "shell"
	// context 的运行时元数据放在 /tmp/korokd/contexts/<contextID> 下
	contextBaseDir  = "/tmp/korokd"
	contextsDirName = "contexts"
	// 以下为运行安全边界与默认值：
	// - contextMaxCount: 单个 korokd 进程允许维护的最大 context 数
	// - contextIdleTTL/contextGCInterval: 空闲回收策略
	// - contextCreateTimeout: 创建后探活超时
	// - context*Timeout*: 执行阶段超时控制
	contextMaxCount           = 32
	contextIdleTTL            = 15 * time.Minute
	contextGCInterval         = 30 * time.Second
	contextCreateTimeout      = 10 * time.Second
	contextDefaultTimeoutMs   = 30000
	contextMinTimeoutMs       = 100
	contextMaxTimeoutMs       = 300000
	contextTimeoutGraceMillis = 2000
	shellExecPollInterval     = 20 * time.Millisecond
)

var (
	errContextNotFound      = fmt.Errorf("context not found")
	errContextBusy          = fmt.Errorf("context is busy")
	errContextLimitExceeded = fmt.Errorf("context limit exceeded")
	errInvalidTimeoutMS     = fmt.Errorf("invalid timeout_ms")
	errCodeRequired         = fmt.Errorf("code is required")
	errCWDOutsideWorkspace  = fmt.Errorf("cwd outside workspace")
	errUnsupportedLanguage  = fmt.Errorf("unsupported language")
)

// kernelContext 表示一个可复用的执行上下文
// python 对应 Jupyter session/kernel，shell 对应常驻 sh 进程，都会在多次执行间保留状态
type kernelContext struct {
	ID         string
	Language   string
	CWD        string
	KernelID   string
	PID        int
	RootDir    string // shell 专用：用于存储临时脚本与输出文件
	cmd        *exec.Cmd
	waitCh     chan error
	shellStdin io.WriteCloser

	createdAt      time.Time
	lastActiveUnix atomic.Int64
	executionCount atomic.Int64
	busy           atomic.Bool
}

type contextManager struct {
	mu       sync.RWMutex
	contexts map[string]*kernelContext
	rootDir  string
	jupyter  *jupyter.Client
}

type executeStreamHooks struct {
	OnStdout         func(text string)
	OnStderr         func(text string)
	OnStatus         func(state string)
	OnExecutionCount func(count int64)
}

func newContextManager() (*contextManager, error) {
	// 1. 准备运行目录
	// 2. 初始化 Jupyter 客户端（指向本容器内的 Jupyter Server）
	// 3. 启动后台 GC，负责回收空闲 context
	rootDir := filepath.Join(contextBaseDir, contextsDirName)
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return nil, fmt.Errorf("create contexts dir failed: %w", err)
	}

	jupyterHost := strings.TrimSpace(os.Getenv("JUPYTER_HOST"))
	if jupyterHost == "" {
		jupyterHost = "http://127.0.0.1:44771"
	}
	jupyterToken := strings.TrimSpace(os.Getenv("JUPYTER_TOKEN"))
	jc, err := jupyter.NewClient(jupyterHost, jupyterToken)
	if err != nil {
		return nil, fmt.Errorf("init jupyter client failed: %w", err)
	}

	m := &contextManager{
		contexts: make(map[string]*kernelContext),
		rootDir:  rootDir,
		jupyter:  jc,
	}

	// 后台协程定时回收空闲 context，限制资源持续增长
	go m.runGC()

	return m, nil
}

func (m *contextManager) runGC() {
	// 周期扫描：
	// - 跳过 busy 的 context（避免中断正在执行的任务）
	// - 对超过空闲阈值的 context 执行强制回收
	ticker := time.NewTicker(contextGCInterval)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		staleIDs := make([]string, 0)
		m.mu.RLock()
		for id, ctx := range m.contexts {
			if ctx.busy.Load() {
				continue
			}
			last := time.Unix(0, ctx.lastActiveUnix.Load())
			if now.Sub(last) > contextIdleTTL {
				staleIDs = append(staleIDs, id)
			}
		}
		m.mu.RUnlock()
		for _, id := range staleIDs {
			// GC 回收失败不影响下一轮扫描
			_ = m.removeContext(id, true)
		}
	}
}

func (m *contextManager) create(language, cwd string) (*kernelContext, error) {
	// 创建流程：
	// 1. 校验 cwd 必须位于 /workspace 内
	// 2. 根据 language 选择运行时（python/shell）
	// 3. 注册到内存 map
	// 4. python 分支会在创建后做 probe 探活
	resolvedCWD, err := resolveContextCWD(cwd)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCWDOutsideWorkspace, err)
	}
	normalizedLanguage := strings.ToLower(strings.TrimSpace(language))

	m.mu.Lock()
	if len(m.contexts) >= contextMaxCount {
		m.mu.Unlock()
		return nil, errContextLimitExceeded
	}

	if normalizedLanguage == contextLanguageShell {
		// shell context：创建独立运行目录
		contextID := uuid.NewString()
		contextRoot := filepath.Join(m.rootDir, contextID)
		if err := os.MkdirAll(contextRoot, 0o700); err != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("create context dir failed: %w", err)
		}

		shellCmd := exec.Command("sh")
		shellCmd.Dir = resolvedCWD
		shellStdin, err := shellCmd.StdinPipe()
		if err != nil {
			m.mu.Unlock()
			_ = os.RemoveAll(contextRoot)
			return nil, fmt.Errorf("create shell stdin pipe failed: %w", err)
		}
		shellStdout, err := shellCmd.StdoutPipe()
		if err != nil {
			m.mu.Unlock()
			_ = os.RemoveAll(contextRoot)
			return nil, fmt.Errorf("create shell stdout pipe failed: %w", err)
		}
		shellStderr, err := shellCmd.StderrPipe()
		if err != nil {
			m.mu.Unlock()
			_ = os.RemoveAll(contextRoot)
			return nil, fmt.Errorf("create shell stderr pipe failed: %w", err)
		}
		if err := shellCmd.Start(); err != nil {
			m.mu.Unlock()
			_ = os.RemoveAll(contextRoot)
			return nil, fmt.Errorf("start shell failed: %w", err)
		}

		waitCh := make(chan error, 1)
		go func() {
			waitCh <- shellCmd.Wait()
			close(waitCh)
		}()
		go func() { _, _ = io.Copy(io.Discard, shellStdout) }()
		go func() { _, _ = io.Copy(io.Discard, shellStderr) }()

		kctx := &kernelContext{
			ID:         contextID,
			Language:   contextLanguageShell,
			CWD:        resolvedCWD,
			PID:        shellCmd.Process.Pid,
			RootDir:    contextRoot,
			cmd:        shellCmd,
			waitCh:     waitCh,
			shellStdin: shellStdin,
			createdAt:  time.Now().UTC(),
		}
		now := time.Now().UnixNano()
		kctx.lastActiveUnix.Store(now)
		m.contexts[contextID] = kctx
		m.mu.Unlock()
		return kctx, nil
	}

	if normalizedLanguage != contextLanguagePython {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", errUnsupportedLanguage, language)
	}

	// python context：创建 Jupyter session/kernel。
	contextID := uuid.NewString()
	notebookPath, err := notebookPathForCWD(contextID, resolvedCWD)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	createCtx, cancel := context.WithTimeout(context.Background(), contextCreateTimeout)
	defer cancel()

	kernelName, err := m.searchPythonKernel(createCtx)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	var sess *jupyter.Session
	for {
		sess, err = m.jupyter.CreateSession(createCtx, contextID, notebookPath, kernelName)
		if err == nil {
			break
		}
		if createCtx.Err() != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("create jupyter session failed: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	actualID := strings.TrimSpace(sess.ID)
	if actualID == "" {
		actualID = contextID
	}
	kernelID := strings.TrimSpace(sess.Kernel.ID)
	if kernelID == "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("jupyter session created but kernel id is empty")
	}

	kctx := &kernelContext{
		ID:        actualID,
		Language:  contextLanguagePython,
		CWD:       resolvedCWD,
		KernelID:  kernelID,
		createdAt: time.Now().UTC(),
	}
	now := time.Now().UnixNano()
	kctx.lastActiveUnix.Store(now)
	m.contexts[actualID] = kctx
	m.mu.Unlock()
	return kctx, nil
}

func (m *contextManager) execute(ctx context.Context, contextID, code string, timeoutMs int) (*models.ExecuteContextResp, error) {
	return m.executeWithHooks(ctx, contextID, code, timeoutMs, nil)
}

func (m *contextManager) executeStreaming(
	ctx context.Context,
	contextID, code string,
	timeoutMs int,
	hooks executeStreamHooks,
) (*models.ExecuteContextResp, error) {
	return m.executeWithHooks(ctx, contextID, code, timeoutMs, &hooks)
}

func (m *contextManager) executeWithHooks(
	ctx context.Context,
	contextID, code string,
	timeoutMs int,
	hooks *executeStreamHooks,
) (*models.ExecuteContextResp, error) {
	// 执行流程：
	// 1. 查找 context 并校验参数
	// 2. busy 原子位做串行保护（同一 context 同时只允许一个执行）
	// 3. 根据 language 走对应执行器
	kctx := m.get(contextID)
	if kctx == nil {
		return nil, errContextNotFound
	}

	if strings.TrimSpace(code) == "" {
		return nil, errCodeRequired
	}

	if timeoutMs == 0 {
		timeoutMs = contextDefaultTimeoutMs
	}

	if timeoutMs < contextMinTimeoutMs || timeoutMs > contextMaxTimeoutMs {
		return nil, fmt.Errorf("%w: timeout_ms must be between 100 and 300000", errInvalidTimeoutMS)
	}

	if !kctx.busy.CompareAndSwap(false, true) {
		return nil, errContextBusy
	}
	// 同一个 context 只能串行执行，避免状态竞争
	defer kctx.busy.Store(false)

	switch kctx.Language {
	case contextLanguagePython:
		return m.executePython(ctx, contextID, kctx, code, timeoutMs, hooks)
	case contextLanguageShell:
		return m.executeShell(ctx, contextID, kctx, code, timeoutMs, hooks)
	default:
		return nil, fmt.Errorf("%w: %s", errUnsupportedLanguage, kctx.Language)
	}
}

func (m *contextManager) executePython(
	ctx context.Context,
	contextID string,
	kctx *kernelContext,
	code string,
	timeoutMs int,
	hooks *executeStreamHooks,
) (*models.ExecuteContextResp, error) {
	// python 执行：
	// - 每次执行前注入 os.chdir(cwd)，严格对齐 cwd 语义
	// - 通过 Jupyter kernel channels websocket 执行并聚合 stdout/stderr
	if m.jupyter == nil {
		return nil, fmt.Errorf("jupyter client is nil")
	}
	start := time.Now()

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs+contextTimeoutGraceMillis)*time.Millisecond)
	defer cancel()

	fullCode, err := withChdirPrelude(kctx.CWD, code)
	if err != nil {
		return nil, err
	}

	var jhooks jupyter.ExecuteHooks
	if hooks != nil {
		jhooks = jupyter.ExecuteHooks{
			OnStdout: hooks.OnStdout,
			OnStderr: hooks.OnStderr,
			OnStatus: hooks.OnStatus,
			OnExecutionCount: func(count int64) {
				if hooks.OnExecutionCount != nil {
					hooks.OnExecutionCount(count)
				}
			},
		}
	}
	result, runErr := m.jupyter.ExecuteStream(execCtx, kctx.KernelID, fullCode, jhooks)
	if runErr != nil && errors.Is(runErr, context.DeadlineExceeded) {
		// 超时后认为 kernel 可能进入不稳定状态，直接回收重建更安全
		_ = m.jupyter.InterruptKernel(context.Background(), kctx.KernelID)
		_ = m.removeContext(contextID, true)
		return &models.ExecuteContextResp{
			ContextID:      contextID,
			ExecutionCount: result.ExecutionCount,
			ExitCode:       124,
			Stdout:         result.Stdout,
			Stderr:         result.Stderr,
			DurationMs:     time.Since(start).Milliseconds(),
		}, nil
	}
	if runErr != nil {
		return nil, fmt.Errorf("kernel execute failed: %w", runErr)
	}

	kctx.lastActiveUnix.Store(time.Now().UnixNano())
	kctx.executionCount.Store(result.ExecutionCount)

	exitCode := int32(0)
	if result.Status == "error" {
		exitCode = 1
	}

	return &models.ExecuteContextResp{
		ContextID:      contextID,
		ExecutionCount: result.ExecutionCount,
		ExitCode:       exitCode,
		Stdout:         result.Stdout,
		Stderr:         result.Stderr,
		DurationMs:     time.Since(start).Milliseconds(),
	}, nil
}

func (m *contextManager) executeShell(
	ctx context.Context,
	contextID string,
	kctx *kernelContext,
	code string,
	timeoutMs int,
	hooks *executeStreamHooks,
) (*models.ExecuteContextResp, error) {
	// shell 执行：
	// - 将代码写入临时脚本
	// - 在同一个常驻 shell 进程中 source，确保变量/函数/目录状态可复用
	// - stdout/stderr/exit_code 通过临时文件回传
	if kctx.shellStdin == nil {
		return nil, fmt.Errorf("shell context stdin is nil")
	}

	execID := uuid.NewString()
	basePath := filepath.Join(kctx.RootDir, "shell-"+execID)
	scriptPath := basePath + ".sh"
	stdoutPath := basePath + ".stdout"
	stderrPath := basePath + ".stderr"
	statusPath := basePath + ".status"

	if err := os.WriteFile(scriptPath, []byte(code), 0o700); err != nil {
		return nil, fmt.Errorf("write shell script failed: %w", err)
	}
	defer func() {
		_ = os.Remove(scriptPath)
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
		_ = os.Remove(statusPath)
	}()

	start := time.Now()
	execCount := kctx.executionCount.Add(1)
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	wrapper := fmt.Sprintf(
		". %s >%s 2>%s; __agentland_ec=$?; printf '%%s' \"$__agentland_ec\" >%s\n",
		shellQuote(scriptPath),
		shellQuote(stdoutPath),
		shellQuote(stderrPath),
		shellQuote(statusPath),
	)
	if _, err := io.WriteString(kctx.shellStdin, wrapper); err != nil {
		return nil, fmt.Errorf("write shell command failed: %w", err)
	}

	if hooks != nil && hooks.OnExecutionCount != nil {
		hooks.OnExecutionCount(execCount)
	}
	if hooks != nil && hooks.OnStatus != nil {
		hooks.OnStatus("running")
	}

	stopTail := make(chan struct{})
	var stopTailOnce sync.Once
	stopTailFn := func() { stopTailOnce.Do(func() { close(stopTail) }) }

	var tailWG sync.WaitGroup
	if hooks != nil && (hooks.OnStdout != nil || hooks.OnStderr != nil) {
		tailWG.Add(2)
		go func() {
			defer tailWG.Done()
			tailFile(execCtx, stdoutPath, hooks.OnStdout, stopTail)
		}()
		go func() {
			defer tailWG.Done()
			tailFile(execCtx, stderrPath, hooks.OnStderr, stopTail)
		}()
	}

	if err := waitForShellResult(execCtx, statusPath, kctx.waitCh); err != nil {
		stopTailFn()
		tailWG.Wait()

		if errors.Is(err, context.DeadlineExceeded) {
			_ = m.removeContext(contextID, true)
			stdout := readFileOrEmpty(stdoutPath)
			stderr := readFileOrEmpty(stderrPath)
			kctx.lastActiveUnix.Store(time.Now().UnixNano())
			return &models.ExecuteContextResp{
				ContextID:      contextID,
				ExecutionCount: execCount,
				ExitCode:       124,
				Stdout:         stdout,
				Stderr:         stderr,
				DurationMs:     time.Since(start).Milliseconds(),
			}, nil
		}
		_ = m.removeContext(contextID, true)
		return nil, fmt.Errorf("wait shell result failed: %w", err)
	}

	stopTailFn()
	tailWG.Wait()

	statusRaw, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, fmt.Errorf("read shell status failed: %w", err)
	}
	statusText := strings.TrimSpace(string(statusRaw))
	parsedExit, err := strconv.Atoi(statusText)
	if err != nil {
		return nil, fmt.Errorf("parse shell exit code failed: %w", err)
	}

	stdout := readFileOrEmpty(stdoutPath)
	stderr := readFileOrEmpty(stderrPath)
	kctx.lastActiveUnix.Store(time.Now().UnixNano())
	if hooks != nil && hooks.OnStatus != nil {
		hooks.OnStatus("complete")
	}
	return &models.ExecuteContextResp{
		ContextID:      contextID,
		ExecutionCount: execCount,
		ExitCode:       int32(parsedExit),
		Stdout:         stdout,
		Stderr:         stderr,
		DurationMs:     time.Since(start).Milliseconds(),
	}, nil
}

func (m *contextManager) removeContext(contextID string, force bool) error {
	// 删除流程：
	// 1. 从 map 摘除（先摘除再关进程，避免新请求并发进来）
	// 2. 尝试优雅 shutdown
	// 3. 发送中断信号，必要时 kill
	// 4. 清理 context 目录
	var kctx *kernelContext

	m.mu.Lock()
	if existing, ok := m.contexts[contextID]; ok {
		kctx = existing
		if !force && kctx.busy.Load() {
			m.mu.Unlock()
			return errContextBusy
		}
		// 先从 map 删除，阻止后续请求命中正在关闭的 context
		delete(m.contexts, contextID)
	}
	m.mu.Unlock()

	if kctx == nil {
		return errContextNotFound
	}

	if kctx.Language == contextLanguagePython {
		// Jupyter server 侧回收 session 即可释放 kernel 资源。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if m.jupyter != nil {
			err := m.jupyter.DeleteSession(shutdownCtx, contextID)
			if err != nil {
				var httpErr *jupyter.HTTPError
				if errors.As(err, &httpErr) && httpErr.Status == 404 {
					// already deleted
				} else {
					// best-effort: still proceed to cleanup local state
				}
			}
		}
	}

	if kctx.cmd != nil && kctx.cmd.Process != nil {
		_ = kctx.cmd.Process.Signal(os.Interrupt)
		if kctx.waitCh != nil {
			select {
			case <-kctx.waitCh:
			case <-time.After(2 * time.Second):
				_ = kctx.cmd.Process.Kill()
				select {
				case <-kctx.waitCh:
				case <-time.After(2 * time.Second):
				}
			}
		} else {
			_ = kctx.cmd.Process.Kill()
		}
	}

	_ = os.RemoveAll(kctx.RootDir)
	return nil
}

func (m *contextManager) get(contextID string) *kernelContext {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.contexts[contextID]
}

func waitForShellResult(ctx context.Context, path string, waitCh <-chan error) error {
	ticker := time.NewTicker(shellExecPollInterval)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case waitErr, ok := <-waitCh:
			if !ok {
				return fmt.Errorf("shell process exited")
			}
			if waitErr != nil {
				return fmt.Errorf("shell process exited: %w", waitErr)
			}
			return fmt.Errorf("shell process exited")
		case <-ticker.C:
		}
	}
}

func readFileOrEmpty(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func tailFile(ctx context.Context, path string, onChunk func(string), stop <-chan struct{}) {
	if onChunk == nil {
		return
	}

	var offset int64
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
		}

		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		size := fi.Size()
		if size < offset {
			offset = 0
		}
		if size <= offset {
			continue
		}

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		_, _ = f.Seek(offset, io.SeekStart)

		maxRead := int64(64 * 1024)
		toRead := size - offset
		if toRead > maxRead {
			toRead = maxRead
		}

		buf := make([]byte, toRead)
		n, _ := f.Read(buf)
		_ = f.Close()

		if n <= 0 {
			continue
		}
		offset += int64(n)
		onChunk(string(buf[:n]))
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func resolveContextCWD(input string) (string, error) {
	// cwd 解析规则：
	// - 空值默认 /workspace
	// - 相对路径按 /workspace 拼接
	// - 绝对路径与相对路径都要经过 Clean
	// - 最终必须仍在 /workspace 内，防止目录穿越
	raw := strings.TrimSpace(input)
	if raw == "" {
		raw = contextWorkspaceRoot
	}
	var candidate string
	if filepath.IsAbs(raw) {
		candidate = filepath.Clean(raw)
	} else {
		candidate = filepath.Clean(filepath.Join(contextWorkspaceRoot, raw))
	}
	root := filepath.Clean(contextWorkspaceRoot)
	if candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return "", fmt.Errorf("cwd must be inside /workspace")
	}
	return candidate, nil
}

func (m *contextManager) searchPythonKernel(ctx context.Context) (string, error) {
	// Prefer a dedicated "python" kernelspec if present; otherwise fallback to python3.
	var specs *jupyter.KernelSpecs
	var err error
	for {
		specs, err = m.jupyter.GetKernelSpecs(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("get kernelspecs failed: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if specs == nil || len(specs.Kernelspecs) == 0 {
		return "", fmt.Errorf("no kernelspecs found")
	}

	var fallback string
	for name, info := range specs.Kernelspecs {
		if info == nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(info.Spec.Language)) != contextLanguagePython {
			continue
		}
		if name == "python3" {
			fallback = "python3"
			continue
		}
		// First non-python3 python kernelspec wins.
		return name, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", fmt.Errorf("no python kernelspec found")
}

func notebookPathForCWD(contextID, cwd string) (string, error) {
	// Jupyter session "path" is relative to the server root (we run Jupyter with notebook-dir=/workspace).
	// Store notebooks under <cwd>/.agentland_contexts/<contextID>.ipynb.
	dir := filepath.Join(cwd, ".agentland_contexts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create notebook dir failed: %w", err)
	}
	abs := filepath.Join(dir, contextID+".ipynb")

	rel, err := filepath.Rel(contextWorkspaceRoot, abs)
	if err != nil {
		return "", fmt.Errorf("rel notebook path failed: %w", err)
	}
	rel = filepath.Clean(rel)
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", errCWDOutsideWorkspace
	}
	return filepath.ToSlash(rel), nil
}

func withChdirPrelude(cwd, code string) (string, error) {
	// Use JSON string encoding as a Python string literal.
	b, err := json.Marshal(cwd)
	if err != nil {
		return "", fmt.Errorf("encode cwd failed: %w", err)
	}
	return "import os\nos.chdir(" + string(b) + ")\n" + code, nil
}
