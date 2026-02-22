package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	// 所有 context 的工作目录必须位于该根目录下，避免访问容器内任意路径
	contextWorkspaceRoot  = "/workspace"
	contextLanguagePython = "python"
	contextLanguageShell  = "shell"
	// context 的运行时元数据放在 /tmp/korokd/contexts/<contextID> 下
	contextBaseDir        = "/tmp/korokd"
	contextsDirName       = "contexts"
	contextHelperFileName = "ipykernel-helper.py"
	// 以下为运行安全边界与默认值：
	// - contextMaxCount: 单个 korokd 进程允许维护的最大 context 数
	// - contextIdleTTL/contextGCInterval: 空闲回收策略
	// - contextCreateTimeout: 创建后探活超时
	// - context*Timeout*: 执行阶段超时控制
	contextMaxCount           = 32
	contextIdleTTL            = 15 * time.Minute
	contextGCInterval         = 30 * time.Second
	contextCreateTimeout      = 10 * time.Second
	contextShutdownTimeoutMs  = 3000
	contextDefaultTimeoutMs   = 30000
	contextMinTimeoutMs       = 100
	contextMaxTimeoutMs       = 300000
	contextTimeoutGraceMillis = 2000
	shellExecPollInterval     = 20 * time.Millisecond
)

var (
	errContextNotFound      = errors.New("context not found")
	errContextBusy          = errors.New("context is busy")
	errContextLimitExceeded = errors.New("context limit exceeded")
	errInvalidTimeoutMS     = errors.New("invalid timeout_ms")
	errCodeRequired         = errors.New("code is required")
	errCWDOutsideWorkspace  = errors.New("cwd outside workspace")
	errUnsupportedLanguage  = errors.New("unsupported language")
)

// kernelConnectionFile 对应 Jupyter connection-file 规范，ipykernel 启动时会读取该文件
// 这里使用 Unix Domain Socket 传输，而不是 tcp 端口监听
type kernelConnectionFile struct {
	ShellPort       int    `json:"shell_port"`
	IOPubPort       int    `json:"iopub_port"`
	StdinPort       int    `json:"stdin_port"`
	ControlPort     int    `json:"control_port"`
	HBPort          int    `json:"hb_port"`
	IP              string `json:"ip"`
	Key             string `json:"key"`
	Transport       string `json:"transport"`
	SignatureScheme string `json:"signature_scheme"`
	KernelName      string `json:"kernel_name"`
}

type helperExecuteResult struct {
	Status         string `json:"status"`
	ExecutionCount int64  `json:"execution_count"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
}

type runHelperOptions struct {
	command        string
	connectionFile string
	codeFile       string
	timeoutMs      int
	cwd            string
	result         any
}

// kernelContext 表示一个可复用的执行上下文
// python 对应常驻 ipykernel，shell 对应常驻 sh 进程，都会在多次执行间保留状态
type kernelContext struct {
	ID           string
	Language     string
	CWD          string
	PID          int
	ConnFilePath string
	RootDir      string
	cmd          *exec.Cmd
	waitCh       chan error
	shellStdin   io.WriteCloser

	createdAt      time.Time
	lastActiveUnix atomic.Int64
	executionCount atomic.Int64
	busy           atomic.Bool
}

type contextManager struct {
	mu         sync.RWMutex
	contexts   map[string]*kernelContext
	helperPath string
	rootDir    string
}

//go:embed ipykernel_helper.py
var helperScriptFS embed.FS

func newContextManager() (*contextManager, error) {
	// 1. 准备运行目录
	// 2. 将 embed 的 helper 脚本落盘为可执行文件
	// 3. 启动后台 GC，负责回收空闲 context
	rootDir := filepath.Join(contextBaseDir, contextsDirName)
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return nil, fmt.Errorf("create contexts dir failed: %w", err)
	}

	helperContent, err := helperScriptFS.ReadFile("ipykernel_helper.py")
	if err != nil {
		return nil, fmt.Errorf("read helper script failed: %w", err)
	}

	helperPath := filepath.Join(contextBaseDir, contextHelperFileName)
	if err := os.WriteFile(helperPath, helperContent, 0o700); err != nil {
		return nil, fmt.Errorf("write helper script failed: %w", err)
	}

	m := &contextManager{
		contexts:   make(map[string]*kernelContext),
		helperPath: helperPath,
		rootDir:    rootDir,
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

	// 为该 context 创建独立运行目录与连接文件
	contextID := uuid.NewString()
	contextRoot := filepath.Join(m.rootDir, contextID)
	if err := os.MkdirAll(contextRoot, 0o700); err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("create context dir failed: %w", err)
	}

	if normalizedLanguage == contextLanguageShell {
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
		_ = os.RemoveAll(contextRoot)
		return nil, fmt.Errorf("%w: %s", errUnsupportedLanguage, language)
	}

	connPath := filepath.Join(contextRoot, "connection.json")
	connFile, err := buildConnectionFile(filepath.Join(contextRoot, "kernel"))
	if err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(contextRoot)
		return nil, fmt.Errorf("build connection file failed: %w", err)
	}

	connBytes, err := json.Marshal(connFile)
	if err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(contextRoot)
		return nil, fmt.Errorf("marshal connection file failed: %w", err)
	}

	if err := os.WriteFile(connPath, connBytes, 0o600); err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(contextRoot)
		return nil, fmt.Errorf("write connection file failed: %w", err)
	}

	cmd := exec.Command("python3", "-m", "ipykernel_launcher", "-f", connPath)
	cmd.Dir = resolvedCWD
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		_ = os.RemoveAll(contextRoot)
		return nil, fmt.Errorf("start ipykernel failed: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
		close(waitCh)
	}()

	kctx := &kernelContext{
		ID:           contextID,
		Language:     contextLanguagePython,
		CWD:          resolvedCWD,
		PID:          cmd.Process.Pid,
		ConnFilePath: connPath,
		RootDir:      contextRoot,
		cmd:          cmd,
		waitCh:       waitCh,
		createdAt:    time.Now().UTC(),
	}

	now := time.Now().UnixNano()
	kctx.lastActiveUnix.Store(now)
	m.contexts[contextID] = kctx
	m.mu.Unlock()

	// 在对外可用前先探活
	probeCtx, cancel := context.WithTimeout(context.Background(), contextCreateTimeout)
	defer cancel()
	if err := m.runHelper(probeCtx, runHelperOptions{
		command:        "probe",
		connectionFile: kctx.ConnFilePath,
		timeoutMs:      5000,
		cwd:            kctx.CWD,
	}); err != nil {
		_ = m.removeContext(contextID, true)
		return nil, fmt.Errorf("ipykernel probe failed: %w", err)
	}

	return kctx, nil
}

func (m *contextManager) execute(ctx context.Context, contextID, code string, timeoutMs int) (*ExecuteContextResp, error) {
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
		return m.executePython(ctx, contextID, kctx, code, timeoutMs)
	case contextLanguageShell:
		return m.executeShell(ctx, contextID, kctx, code, timeoutMs)
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
) (*ExecuteContextResp, error) {
	// python 执行：
	// - 写入 code.py
	// - 调用 helper 与 ipykernel 通信
	// - 解析 helper 状态并映射 exit_code
	codePath := filepath.Join(kctx.RootDir, "code.py")
	if err := os.WriteFile(codePath, []byte(code), 0o600); err != nil {
		return nil, fmt.Errorf("write code file failed: %w", err)
	}

	start := time.Now()
	helperResult := &helperExecuteResult{}
	helperCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs+contextTimeoutGraceMillis)*time.Millisecond)
	defer cancel()

	runErr := m.runHelper(helperCtx, runHelperOptions{
		command:        "execute",
		connectionFile: kctx.ConnFilePath,
		codeFile:       codePath,
		timeoutMs:      timeoutMs,
		cwd:            kctx.CWD,
		result:         helperResult,
	})
	_ = os.Remove(codePath)
	if runErr != nil {
		return nil, fmt.Errorf("kernel execute failed: %w", runErr)
	}

	kctx.lastActiveUnix.Store(time.Now().UnixNano())
	kctx.executionCount.Store(helperResult.ExecutionCount)

	exitCode := int32(0)
	switch helperResult.Status {
	case "ok":
		exitCode = 0
	case "error":
		exitCode = 1
	case "timeout":
		exitCode = 124
		// 超时后认为 kernel 可能进入不稳定状态，直接回收重建更安全
		_ = m.removeContext(contextID, true)
	default:
		return nil, fmt.Errorf("invalid helper status: %s", helperResult.Status)
	}

	return &ExecuteContextResp{
		ContextID:      contextID,
		ExecutionCount: helperResult.ExecutionCount,
		ExitCode:       exitCode,
		Stdout:         helperResult.Stdout,
		Stderr:         helperResult.Stderr,
		DurationMs:     time.Since(start).Milliseconds(),
	}, nil
}

func (m *contextManager) executeShell(
	ctx context.Context,
	contextID string,
	kctx *kernelContext,
	code string,
	timeoutMs int,
) (*ExecuteContextResp, error) {
	// shell 执行：
	// - 将代码写入临时脚本
	// - 在同一个常驻 shell 进程中 source，确保变量/函数/目录状态可复用
	// - stdout/stderr/exit_code 通过临时文件回传
	if kctx.shellStdin == nil {
		return nil, errors.New("shell context stdin is nil")
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

	if err := waitForShellResult(execCtx, statusPath, kctx.waitCh); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			_ = m.removeContext(contextID, true)
			stdout := readFileOrEmpty(stdoutPath)
			stderr := readFileOrEmpty(stderrPath)
			kctx.lastActiveUnix.Store(time.Now().UnixNano())
			return &ExecuteContextResp{
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
	return &ExecuteContextResp{
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// 优先走协议层 shutdown，再走进程信号兜底
		_ = m.runHelper(shutdownCtx, runHelperOptions{
			command:        "shutdown",
			connectionFile: kctx.ConnFilePath,
			timeoutMs:      contextShutdownTimeoutMs,
			cwd:            kctx.CWD,
		})
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

func (m *contextManager) runHelper(
	ctx context.Context,
	opts runHelperOptions,
) error {
	// helper 脚本是 Go 与 Jupyter 协议之间的桥接层：
	// - probe: 探测 kernel 是否就绪
	// - execute: 执行代码并返回 stdout/stderr/status
	// - shutdown: 请求 kernel 优雅关闭
	args := []string{
		m.helperPath,
		opts.command,
		"--connection-file", opts.connectionFile,
		"--timeout-ms", strconv.Itoa(opts.timeoutMs),
	}
	if opts.codeFile != "" {
		args = append(args, "--code-file", opts.codeFile)
	}
	cmd := exec.CommandContext(ctx, "python3", args...)
	cmd.Dir = opts.cwd
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run helper failed: %w: stderr=%s", err, strings.TrimSpace(stderr.String()))
	}
	if opts.result != nil {
		// execute/probe 的输出是 JSON，反序列化给调用方结构体
		if err := json.Unmarshal(stdout.Bytes(), opts.result); err != nil {
			return fmt.Errorf("decode helper output failed: %w", err)
		}
	}
	return nil
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
				return errors.New("shell process exited")
			}
			if waitErr != nil {
				return fmt.Errorf("shell process exited: %w", waitErr)
			}
			return errors.New("shell process exited")
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
		return "", errors.New("cwd must be inside /workspace")
	}
	return candidate, nil
}

func buildConnectionFile(ipBase string) (*kernelConnectionFile, error) {
	// 构造 ipykernel 连接信息：
	// - 生成会话签名 key
	// - 分配 5 个 channel 的端点标识
	// - transport 固定 ipc（对应 UDS）
	key, err := randomKey()
	if err != nil {
		return nil, err
	}
	nextPort := func() (int, error) {
		// 生成 [10000, 59999] 区间的随机编号
		// 在 ipc 模式下，这些值会被拼接成 socket 文件名标识
		n, err := rand.Int(rand.Reader, big.NewInt(50000))
		if err != nil {
			return 0, err
		}
		return int(n.Int64()) + 10000, nil
	}
	shellPort, err := nextPort()
	if err != nil {
		return nil, err
	}
	iopubPort, err := nextPort()
	if err != nil {
		return nil, err
	}
	stdinPort, err := nextPort()
	if err != nil {
		return nil, err
	}
	controlPort, err := nextPort()
	if err != nil {
		return nil, err
	}
	hbPort, err := nextPort()
	if err != nil {
		return nil, err
	}
	return &kernelConnectionFile{
		ShellPort:       shellPort,
		IOPubPort:       iopubPort,
		StdinPort:       stdinPort,
		ControlPort:     controlPort,
		HBPort:          hbPort,
		IP:              filepath.ToSlash(ipBase),
		Key:             key,
		Transport:       "ipc",
		SignatureScheme: "hmac-sha256",
		KernelName:      "python3",
	}, nil
}

func randomKey() (string, error) {
	// 生成 Jupyter 消息签名密钥
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
