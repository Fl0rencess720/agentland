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
	contextWorkspaceRoot = "/workspace"
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
)

var (
	errContextNotFound      = errors.New("context not found")
	errContextBusy          = errors.New("context is busy")
	errContextLimitExceeded = errors.New("context limit exceeded")
	errInvalidTimeoutMS     = errors.New("invalid timeout_ms")
	errCodeRequired         = errors.New("code is required")
	errCWDOutsideWorkspace  = errors.New("cwd outside workspace")
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

// kernelContext 表示一个可复用的 Python 执行上下文
// 一个 context 对应一个常驻 ipykernel 进程，变量状态会在多次执行之间保留
type kernelContext struct {
	ID           string
	Language     string
	CWD          string
	PID          int
	ConnFilePath string
	RootDir      string
	cmd          *exec.Cmd
	waitCh       chan error

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

func (m *contextManager) create(cwd string) (*kernelContext, error) {
	// 创建流程：
	// 1. 校验 cwd 必须位于 /workspace 内
	// 2. 分配 context 目录与 connection.json
	// 3. 启动 ipykernel 子进程
	// 4. 注册到内存 map
	// 5. probe 探活，确保 kernel 已可用
	resolvedCWD, err := resolveContextCWD(cwd)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCWDOutsideWorkspace, err)
	}

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
		Language:     "python",
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
	// 3. 将代码写入临时文件，由 helper 发给 ipykernel 执行
	// 4. 解析执行结果并映射 exit_code
	// 5. timeout 时主动回收 context（防止异常状态继续复用）
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 优先走协议层 shutdown，再走进程信号兜底
	_ = m.runHelper(shutdownCtx, runHelperOptions{
		command:        "shutdown",
		connectionFile: kctx.ConnFilePath,
		timeoutMs:      contextShutdownTimeoutMs,
		cwd:            kctx.CWD,
	})

	if kctx.cmd != nil && kctx.cmd.Process != nil {
		_ = kctx.cmd.Process.Signal(os.Interrupt)
		select {
		case <-kctx.waitCh:
		case <-time.After(2 * time.Second):
			_ = kctx.cmd.Process.Kill()
			select {
			case <-kctx.waitCh:
			case <-time.After(2 * time.Second):
			}
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
