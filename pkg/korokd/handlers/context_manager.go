package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/Fl0rencess720/agentland/pkg/korokd/pkgs/jupyter"
	utils "github.com/Fl0rencess720/agentland/pkg/korokd/pkgs/utils"
	"github.com/google/uuid"
)

const (
	// 所有 context 的工作目录必须位于该根目录下，避免访问容器内任意路径
	contextWorkspaceRoot  = "/workspace"
	contextLanguagePython = "python"
	contextLanguageBash   = "bash"
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
)

var (
	errContextNotFound      = fmt.Errorf("context not found")
	errContextBusy          = fmt.Errorf("context is busy")
	errContextLimitExceeded = fmt.Errorf("context limit exceeded")
	errInvalidTimeoutMS     = fmt.Errorf("invalid timeout_ms")
	errCWDOutsideWorkspace  = fmt.Errorf("cwd outside workspace")
	errUnsupportedLanguage  = fmt.Errorf("unsupported language")
)

// kernelContext 表示一个可复用的执行上下文
// python/bash 对应 Jupyter session/kernel，都会在多次执行间保留状态
type kernelContext struct {
	ID       string
	Language string
	CWD      string
	KernelID string

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
	// 2. 根据 language 选择运行时（python/bash）
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

	if normalizedLanguage != contextLanguagePython && normalizedLanguage != contextLanguageBash {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", errUnsupportedLanguage, language)
	}

	// python/bash context：创建 Jupyter session/kernel。
	contextID := uuid.NewString()
	notebookPath, err := notebookPathForCWD(contextID, resolvedCWD)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	createCtx, cancel := context.WithTimeout(context.Background(), contextCreateTimeout)
	defer cancel()

	kernelName, err := m.searchKernel(createCtx, normalizedLanguage)
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
		Language:  normalizedLanguage,
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
	case contextLanguageBash:
		return m.executeBash(ctx, contextID, kctx, code, timeoutMs, hooks)
	default:
		return nil, fmt.Errorf("%w: %s", errUnsupportedLanguage, kctx.Language)
	}
}

func toJupyterHooks(hooks *executeStreamHooks) jupyter.ExecuteHooks {
	if hooks == nil {
		return jupyter.ExecuteHooks{}
	}
	return jupyter.ExecuteHooks{
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

func (m *contextManager) executePython(
	ctx context.Context,
	contextID string,
	kctx *kernelContext,
	code string,
	timeoutMs int,
	hooks *executeStreamHooks,
) (*models.ExecuteContextResp, error) {
	// python 执行：
	// - 仅在第一次执行前注入 os.chdir(cwd)，之后允许用户自行 os.chdir 并在后续执行中保持
	// - 通过 Jupyter kernel channels websocket 执行并聚合 stdout/stderr
	if m.jupyter == nil {
		return nil, fmt.Errorf("jupyter client is nil")
	}
	start := time.Now()

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs+contextTimeoutGraceMillis)*time.Millisecond)
	defer cancel()

	fullCode, err := withPythonInit(kctx.CWD, code)
	if err != nil {
		return nil, err
	}

	jhooks := toJupyterHooks(hooks)
	result, runErr := m.jupyter.Execute(execCtx, kctx.KernelID, fullCode, jhooks)
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

func (m *contextManager) executeBash(
	ctx context.Context,
	contextID string,
	kctx *kernelContext,
	code string,
	timeoutMs int,
	hooks *executeStreamHooks,
) (*models.ExecuteContextResp, error) {
	// bash 执行（Jupyter bash_kernel）：
	// - 使用同一个 kernel session，变量/函数/cwd 等状态跨多次执行保留
	// - 为保持与历史 shell→bash 迁移语义对齐：仅在第一次执行时 cd 到创建 context 的 cwd（后续允许用户 cd 持久化）
	// - 追加一个服务端 marker 行携带 exit_code，并在 SSE 与最终 stdout 中剥离
	if m.jupyter == nil {
		return nil, fmt.Errorf("jupyter client is nil")
	}
	start := time.Now()
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs+contextTimeoutGraceMillis)*time.Millisecond)
	defer cancel()

	markerKey := utils.BashExitMarkerPrefix + uuid.NewString()
	wrapped := withBashInit(kctx.CWD, code, markerKey)

	filter := utils.NewBashExitCodeFilter(markerKey)
	jhooks := toJupyterHooks(hooks)
	var stdoutDownstream func(string)
	if jhooks.OnStdout != nil {
		stdoutDownstream = jhooks.OnStdout
		jhooks.OnStdout = func(text string) {
			if out := filter.HandleChunk(text); out != "" {
				stdoutDownstream(out)
			}
		}
	}

	result, runErr := m.jupyter.Execute(execCtx, kctx.KernelID, wrapped, jhooks)
	if stdoutDownstream != nil {
		if out := filter.Flush(); out != "" {
			stdoutDownstream(out)
		}
	}

	if runErr != nil && errors.Is(runErr, context.DeadlineExceeded) {
		_ = m.jupyter.InterruptKernel(context.Background(), kctx.KernelID)
		_ = m.removeContext(contextID, true)
		return &models.ExecuteContextResp{
			ContextID:      contextID,
			ExecutionCount: result.ExecutionCount,
			ExitCode:       124,
			Stdout:         utils.StripExitMarker(result.Stdout, markerKey),
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
	if parsed, ok := utils.ParseExitMarker(result.Stdout, markerKey); ok {
		exitCode = parsed
	} else if parsed, ok := filter.ExitCode(); ok {
		exitCode = parsed
	} else if result.Status == "error" {
		exitCode = 1
	}

	return &models.ExecuteContextResp{
		ContextID:      contextID,
		ExecutionCount: result.ExecutionCount,
		ExitCode:       exitCode,
		Stdout:         utils.StripExitMarker(result.Stdout, markerKey),
		Stderr:         result.Stderr,
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

	// Jupyter server 侧回收 session 即可释放 kernel 资源（python/bash 同构）。
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
	return nil
}

func (m *contextManager) get(contextID string) *kernelContext {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.contexts[contextID]
}

func (m *contextManager) searchKernel(ctx context.Context, normalizedLanguage string) (string, error) {
	specs, err := m.getKernelSpecsWithRetry(ctx)
	if err != nil {
		return "", err
	}

	switch normalizedLanguage {
	case contextLanguagePython:
		// 优先使用显式注册的 kernelspec（"python"），找不到则回退到 "python3"。
		return pickKernelBySpecLanguage(specs, contextLanguagePython, []string{"python"}, map[string]struct{}{"python3": {}}, "python3")
	case contextLanguageBash:
		// 优先使用常见的 bash kernelspec 名称；否则选择任意 bash kernelspec。
		return pickKernelBySpecLanguage(specs, contextLanguageBash, []string{"bash", "bash_kernel"}, nil, "")
	default:
		return "", fmt.Errorf("%w: %s", errUnsupportedLanguage, normalizedLanguage)
	}
}

func (m *contextManager) getKernelSpecsWithRetry(ctx context.Context) (*jupyter.KernelSpecs, error) {
	if m.jupyter == nil {
		return nil, fmt.Errorf("jupyter client is nil")
	}

	var specs *jupyter.KernelSpecs
	var err error
	for {
		specs, err = m.jupyter.GetKernelSpecs(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("get kernelspecs failed: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if specs == nil || len(specs.Kernelspecs) == 0 {
		return nil, fmt.Errorf("no kernelspecs found")
	}
	return specs, nil
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

func pickKernelBySpecLanguage(
	specs *jupyter.KernelSpecs,
	specLanguage string,
	preferNames []string,
	avoidNames map[string]struct{},
	fallbackName string,
) (string, error) {
	// 先尝试按名称精确匹配（名称稳定，不受 map 遍历顺序影响）。
	for _, name := range preferNames {
		info := specs.Kernelspecs[name]
		if info == nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(info.Spec.Language)) == specLanguage {
			return name, nil
		}
	}

	hasFallback := false
	for name, info := range specs.Kernelspecs {
		if info == nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(info.Spec.Language)) != specLanguage {
			continue
		}
		if fallbackName != "" && name == fallbackName {
			hasFallback = true
			continue
		}
		if avoidNames != nil {
			if _, bad := avoidNames[name]; bad {
				continue
			}
		}
		return name, nil
	}

	if fallbackName != "" && hasFallback {
		return fallbackName, nil
	}
	return "", fmt.Errorf("no kernelspec found for language=%s", specLanguage)
}

func notebookPathForCWD(contextID, cwd string) (string, error) {
	// Jupyter session 的 "path" 相对 Jupyter server root（我们使用 notebook-dir=/workspace）。
	// 将 notebook 存到 <cwd>/.agentland_contexts/<contextID>.ipynb。
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

func withPythonInit(cwd, code string) (string, error) {
	// 使用 JSON 字符串编码，保证可作为 Python 字符串字面量安全拼接。
	b, err := json.Marshal(cwd)
	if err != nil {
		return "", fmt.Errorf("encode cwd failed: %w", err)
	}
	// Initialize cwd only once for this kernel session; allow later `os.chdir` to persist across executions.
	// This keeps "interactive Python" semantics closer to bash.
	return strings.Join([]string{
		"import os",
		"if '__agentland_cwd_inited' not in globals():",
		"\tos.chdir(" + string(b) + ")",
		"\t__agentland_cwd_inited = True",
		code,
	}, "\n") + "\n", nil
}

func withBashInit(cwd, code, markerKey string) string {
	// 仅在本 kernel session 第一次执行时初始化 cwd；之后允许用户 `cd` 并在后续执行中保持。
	// 在输出中追加一行包含 exit_code 的 marker（服务端会在 SSE 与最终 stdout 中剥离）。
	quotedCWD := shellQuote(cwd)
	quotedMarkerKey := shellQuote(markerKey)
	return strings.Join([]string{
		`if [ -z "${__agentland_cwd_inited+x}" ]; then cd ` + quotedCWD + `; __agentland_cwd_inited=1; fi`,
		code,
		`__agentland_ec=$?`,
		`printf '%s=%s\n' ` + quotedMarkerKey + ` "$__agentland_ec"`,
	}, "\n") + "\n"
}
