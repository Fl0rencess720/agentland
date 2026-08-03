package agentd

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

type localTools struct {
	root    string
	skills  *SkillRegistry
	writeMu sync.Mutex
}

type shellInput struct {
	Command string `json:"command"`
	CWD     string `json:"cwd,omitempty"`
}

type shellOutput struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	LogPath  string `json:"log_path,omitempty"`
}

const (
	maxShellOutputBytes   = 32 << 10
	maxEditableFileBytes  = 4 << 20
	maxListFileResults    = 2048
	maxListFileResultSize = maxToolOutputBytes
	maxGrepMatches        = 1024
	maxGrepResultSize     = maxToolOutputBytes
	maxGrepLineBytes      = 64 << 10
)

var (
	errListFileLimit     = errors.New("file list limit reached")
	errGrepMatchLimit    = errors.New("grep result limit reached")
	errWorkspaceInternal = errors.New("path is internal to agentd")
)

type shellCapture struct {
	file    *os.File
	path    string
	prefix  []byte
	written int64
	mu      sync.Mutex
}

type pathInput struct {
	Path string `json:"path"`
}

type writeFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type editFileInput struct {
	Path       string `json:"path"`
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type grepInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

type grepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type readSkillInput struct {
	Name string `json:"name"`
}

func NewLocalTools(workspaceRoot string, skills *SkillRegistry) ([]tool.BaseTool, error) {
	tools, _, err := newLocalTools(workspaceRoot, skills)
	return tools, err
}

func newLocalTools(workspaceRoot string, skills *SkillRegistry) ([]tool.BaseTool, *localTools, error) {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create workspace root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	t := &localTools{root: filepath.Clean(root), skills: skills}
	if err := t.prepareWorkspaceDir(t.root); err != nil {
		return nil, nil, err
	}
	constructors := []func() (tool.InvokableTool, error){
		func() (tool.InvokableTool, error) {
			return toolutils.InferTool("shell", "Run a Bash command inside the workspace. Use this for builds, tests, package managers, git, Python, Node.js, and agent-browser.", t.shell)
		},
		func() (tool.InvokableTool, error) {
			return toolutils.InferTool("read_file", "Read a UTF-8 text file from the workspace.", t.readFile)
		},
		func() (tool.InvokableTool, error) {
			return toolutils.InferTool("write_file", "Create or replace a file in the workspace.", t.writeFile)
		},
		func() (tool.InvokableTool, error) {
			return toolutils.InferTool("edit_file", "Replace exact text in a workspace file. The old text must occur once unless replace_all is true.", t.editFile)
		},
		func() (tool.InvokableTool, error) {
			return toolutils.InferTool("list_files", "Recursively list files under a workspace path.", t.listFiles)
		},
		func() (tool.InvokableTool, error) {
			return toolutils.InferTool("grep", "Search workspace text files with a Go regular expression.", t.grep)
		},
		func() (tool.InvokableTool, error) {
			return toolutils.InferTool("read_skill", "Load the complete instructions for an available skill.", t.readSkill)
		},
	}

	tools := make([]tool.BaseTool, 0, len(constructors))
	for _, constructor := range constructors {
		item, err := constructor()
		if err != nil {
			return nil, nil, err
		}
		tools = append(tools, item)
	}
	return tools, t, nil
}

func (t *localTools) shell(ctx context.Context, input shellInput) (shellOutput, error) {
	if strings.TrimSpace(input.Command) == "" {
		return shellOutput{}, fmt.Errorf("command is required")
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.prepareWorkspaceDir(t.root); err != nil {
		return shellOutput{}, err
	}
	cwd := input.CWD
	if cwd == "" {
		cwd = t.root
	}
	resolvedCWD, err := t.resolveExisting(cwd)
	if err != nil {
		return shellOutput{}, err
	}
	info, err := os.Stat(resolvedCWD)
	if err != nil {
		return shellOutput{}, err
	}
	if !info.IsDir() {
		return shellOutput{}, fmt.Errorf("cwd is not a directory")
	}

	cmd := exec.CommandContext(ctx, "bash", "-lc", input.Command)
	cmd.Dir = resolvedCWD
	cmd.Env, err = childProcessEnv(nil)
	if err != nil {
		return shellOutput{}, err
	}
	cmd.SysProcAttr, err = childProcessSysProcAttr(true)
	if err != nil {
		return shellOutput{}, err
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	capture, err := t.newShellCapture()
	if err != nil {
		return shellOutput{}, err
	}
	cmd.Stdout = capture
	cmd.Stderr = capture
	runErr := cmd.Run()
	result, captureErr := capture.finish(t)
	if captureErr != nil {
		return shellOutput{}, captureErr
	}
	err = runErr
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return shellOutput{}, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return shellOutput{}, err
}

func (t *localTools) newShellCapture() (*shellCapture, error) {
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	defer root.Close()
	logDir := filepath.Join(".agentland", "logs")
	if err := root.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create shell log directory: %w", err)
	}
	file, logPath, err := createRootTemp(root, logDir, "shell-", 0o600)
	if err != nil {
		return nil, fmt.Errorf("create shell log: %w", err)
	}
	return &shellCapture{file: file, path: logPath, prefix: make([]byte, 0, maxShellOutputBytes)}, nil
}

func (c *shellCapture) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.file.Write(data)
	c.written += int64(n)
	if remaining := maxShellOutputBytes - len(c.prefix); remaining > 0 {
		c.prefix = append(c.prefix, data[:min(n, remaining)]...)
	}
	return n, err
}

func (c *shellCapture) finish(tools *localTools) (shellOutput, error) {
	if err := c.file.Close(); err != nil {
		tools.remove(c.path)
		return shellOutput{}, fmt.Errorf("close shell log: %w", err)
	}
	output := strings.ToValidUTF8(string(c.prefix), "\uFFFD")
	if c.written <= maxShellOutputBytes {
		if err := tools.remove(c.path); err != nil {
			return shellOutput{}, fmt.Errorf("remove temporary shell log: %w", err)
		}
		return shellOutput{Output: output}, nil
	}

	logPath := filepath.ToSlash(c.path)
	output += fmt.Sprintf("\n\n[output truncated after %d bytes; full log: %s]", maxShellOutputBytes, logPath)
	return shellOutput{Output: output, LogPath: logPath}, nil
}

func (t *localTools) readFile(_ context.Context, input pathInput) (string, error) {
	name, err := t.workspaceName(input.Path)
	if err != nil {
		return "", err
	}
	if err := ensureInternalAccess(name, true); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := t.openRootFile(root, name, true)
	if err != nil {
		return "", workspaceRootError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxToolOutputBytes+1))
	if err != nil {
		return "", err
	}
	originalSize := max(info.Size(), int64(len(data)))
	if len(data) > maxToolOutputBytes {
		data = data[:maxToolOutputBytes]
	}
	return boundToolOutputSize(string(data), originalSize), nil
}

func (t *localTools) writeFile(_ context.Context, input writeFileInput) (string, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.writeFileUnlocked(input)
}

func (t *localTools) writeFileUnlocked(input writeFileInput) (string, error) {
	name, err := t.workspaceName(input.Path)
	if err != nil {
		return "", err
	}
	if err := ensureInternalAccess(name, false); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return "", fmt.Errorf("open workspace root: %w", err)
	}
	defer root.Close()
	parentName := filepath.Dir(name)
	if err := t.prepareWorkspaceDirAt(root, parentName); err != nil {
		return "", err
	}
	parentRoot, err := t.openRootDir(root, parentName, false)
	if err != nil {
		return "", fmt.Errorf("open workspace parent: %w", err)
	}
	defer parentRoot.Close()
	tmp, tmpName, err := createRootTemp(parentRoot, ".", ".agentd-write-", 0o644)
	if err != nil {
		return "", err
	}
	defer parentRoot.Remove(tmpName)
	if err := chownFileForTool(tmp); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := io.WriteString(tmp, input.Content); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := parentRoot.Rename(tmpName, filepath.Base(name)); err != nil {
		return "", workspaceRootError(err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(input.Content), filepath.ToSlash(name)), nil
}

func (t *localTools) editFile(_ context.Context, input editFileInput) (string, error) {
	if input.OldText == "" {
		return "", fmt.Errorf("old_text is required")
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	name, err := t.workspaceName(input.Path)
	if err != nil {
		return "", err
	}
	if err := ensureInternalAccess(name, false); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return "", err
	}
	file, err := t.openRootFile(root, name, false)
	if err != nil {
		root.Close()
		return "", workspaceRootError(err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		root.Close()
		return "", err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		root.Close()
		return "", fmt.Errorf("path is not a regular file")
	}
	if info.Size() > maxEditableFileBytes {
		file.Close()
		root.Close()
		return "", fmt.Errorf("file is too large to edit: %d bytes (limit %d)", info.Size(), maxEditableFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxEditableFileBytes+1))
	closeErr := file.Close()
	root.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(data) > maxEditableFileBytes {
		return "", fmt.Errorf("file is too large to edit (limit %d bytes)", maxEditableFileBytes)
	}
	content := string(data)
	count := strings.Count(content, input.OldText)
	if count == 0 {
		return "", fmt.Errorf("old_text was not found")
	}
	if count > 1 && !input.ReplaceAll {
		return "", fmt.Errorf("old_text occurs %d times; provide more context or set replace_all", count)
	}
	limit := 1
	if input.ReplaceAll {
		limit = -1
	}
	replacements := min(count, 1)
	if input.ReplaceAll {
		replacements = count
	}
	if len(input.NewText) > maxEditableFileBytes {
		return "", fmt.Errorf("replacement is too large (limit %d bytes)", maxEditableFileBytes)
	}
	resultSize := int64(len(content)-replacements*len(input.OldText)) + int64(replacements)*int64(len(input.NewText))
	if resultSize > maxEditableFileBytes {
		return "", fmt.Errorf("edited file would exceed %d bytes", maxEditableFileBytes)
	}
	_, err = t.writeFileUnlocked(writeFileInput{
		Path:    name,
		Content: strings.Replace(content, input.OldText, input.NewText, limit),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", count, filepath.ToSlash(name)), nil
}

func (t *localTools) listFiles(_ context.Context, input pathInput) ([]string, error) {
	path := input.Path
	if path == "" {
		path = "."
	}
	name, err := t.workspaceName(path)
	if err != nil {
		return nil, err
	}
	if err := ensureInternalAccess(name, false); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	start, err := t.openRootFile(root, name, false)
	if err != nil {
		return nil, err
	}
	start.Close()
	var files []string
	totalBytes := 0
	err = fs.WalkDir(root.FS(), name, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return workspaceRootError(walkErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if path != name {
				if _, ignored := workspaceTreeIgnoredDirs[entry.Name()]; ignored {
					return filepath.SkipDir
				}
			}
			return nil
		}
		relative := filepath.ToSlash(path)
		if len(files) >= maxListFileResults || totalBytes+len(relative) > maxListFileResultSize {
			return errListFileLimit
		}
		files = append(files, relative)
		totalBytes += len(relative)
		return nil
	})
	if errors.Is(err, errListFileLimit) {
		return nil, fmt.Errorf("file list exceeds %d entries or %d bytes; narrow path", maxListFileResults, maxListFileResultSize)
	}
	sort.Strings(files)
	return files, err
}

func (t *localTools) grep(_ context.Context, input grepInput) ([]grepMatch, error) {
	pattern, err := regexp.Compile(input.Pattern)
	if err != nil {
		return nil, fmt.Errorf("compile pattern: %w", err)
	}
	path := input.Path
	if path == "" {
		path = "."
	}
	name, err := t.workspaceName(path)
	if err != nil {
		return nil, err
	}
	if err := ensureInternalAccess(name, false); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	start, err := t.openRootFile(root, name, false)
	if err != nil {
		return nil, err
	}
	start.Close()
	var matches []grepMatch
	totalBytes := 0
	err = fs.WalkDir(root.FS(), name, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return workspaceRootError(walkErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if path != name {
				if _, ignored := workspaceTreeIgnoredDirs[entry.Name()]; ignored {
					return filepath.SkipDir
				}
			}
			return nil
		}
		file, err := t.openRootFile(root, path, false)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 32<<10), maxGrepLineBytes)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			if strings.IndexByte(text, 0) >= 0 {
				break
			}
			if pattern.MatchString(text) {
				relative := filepath.ToSlash(path)
				matchBytes := len(relative) + len(text)
				if len(matches) >= maxGrepMatches || totalBytes+matchBytes > maxGrepResultSize {
					_ = file.Close()
					return errGrepMatchLimit
				}
				matches = append(matches, grepMatch{Path: relative, Line: line, Text: text})
				totalBytes += matchBytes
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return fmt.Errorf("scan %s: %w", filepath.ToSlash(path), scanErr)
		}
		return closeErr
	})
	if errors.Is(err, errGrepMatchLimit) {
		return nil, fmt.Errorf("grep exceeds %d matches or %d bytes; narrow pattern or path", maxGrepMatches, maxGrepResultSize)
	}
	return matches, err
}

func (t *localTools) readSkill(_ context.Context, input readSkillInput) (string, error) {
	if t.skills == nil {
		return "", fmt.Errorf("skills are unavailable")
	}
	return t.skills.Read(input.Name)
}

func (t *localTools) resolveExisting(path string) (string, error) {
	name, err := t.workspaceName(path)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		return "", workspaceRootError(err)
	}
	if info.IsDir() {
		dir, err := root.OpenRoot(name)
		if err != nil {
			return "", workspaceRootError(err)
		}
		dir.Close()
	} else {
		file, err := root.Open(name)
		if err != nil {
			return "", workspaceRootError(err)
		}
		file.Close()
	}
	return filepath.Join(t.root, name), nil
}

func (t *localTools) candidate(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(t.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if !withinRoot(t.root, candidate) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return candidate, nil
}

func (t *localTools) workspaceName(path string) (string, error) {
	candidate, err := t.candidate(path)
	if err != nil {
		return "", err
	}
	name, err := filepath.Rel(t.root, candidate)
	if err != nil || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	if name == "" {
		return ".", nil
	}
	return name, nil
}

func (t *localTools) prepareWorkspaceDir(path string) error {
	name, err := t.workspaceName(path)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return err
	}
	defer root.Close()
	return t.prepareWorkspaceDirAt(root, name)
}

func (t *localTools) prepareWorkspaceDirAt(root *os.Root, name string) error {
	mode := os.FileMode(0o755)
	if isAgentlandName(name) {
		mode = 0o700
	}
	info, err := root.Stat(name)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("workspace parent is not a directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := root.MkdirAll(name, mode); err != nil {
			return workspaceRootError(err)
		}
	} else {
		return workspaceRootError(err)
	}
	if isAgentlandName(name) {
		return nil
	}
	dirs := []string{"."}
	current := "."
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		if part != "." {
			current = filepath.Join(current, part)
			dirs = append(dirs, current)
		}
	}
	for _, current := range dirs {
		dir, err := t.openRootFile(root, current, false)
		if err != nil {
			return workspaceRootError(err)
		}
		if err := dir.Chmod(0o755); err != nil {
			dir.Close()
			return err
		}
		if err := chownFileForTool(dir); err != nil {
			dir.Close()
			return err
		}
		if err := dir.Close(); err != nil {
			return err
		}
	}
	return nil
}

func isAgentlandName(name string) bool {
	return name == ".agentland" || strings.HasPrefix(name, ".agentland"+string(filepath.Separator))
}

func isAgentlandLogName(name string) bool {
	prefix := filepath.Join(".agentland", "logs") + string(filepath.Separator)
	return strings.HasPrefix(name, prefix)
}

func ensureInternalAccess(name string, allowLogs bool) error {
	if !isAgentlandName(name) || (allowLogs && isAgentlandLogName(name)) {
		return nil
	}
	return errWorkspaceInternal
}

func (t *localTools) openRootFile(root *os.Root, name string, allowLogs bool) (*os.File, error) {
	if err := ensureInternalAccess(name, allowLogs); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, workspaceRootError(err)
	}
	if err := t.validateOpenedPath(file, allowLogs); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (t *localTools) openRootDir(root *os.Root, name string, allowLogs bool) (*os.Root, error) {
	if err := ensureInternalAccess(name, allowLogs); err != nil {
		return nil, err
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return nil, workspaceRootError(err)
	}
	marker, err := dir.Open(".")
	if err != nil {
		dir.Close()
		return nil, workspaceRootError(err)
	}
	err = t.validateOpenedPath(marker, allowLogs)
	marker.Close()
	if err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}

func (t *localTools) validateOpenedPath(file *os.File, allowLogs bool) error {
	resolved, err := fileDescriptorPath(file)
	if err != nil {
		return fmt.Errorf("resolve opened workspace file: %w", err)
	}
	resolved = strings.TrimSuffix(resolved, " (deleted)")
	agentland := filepath.Join(t.root, ".agentland")
	logs := filepath.Join(agentland, "logs")
	internal := withinRoot(agentland, resolved)
	allowedLog := allowLogs && withinRoot(logs, resolved) && resolved != logs
	if internal && !allowedLog {
		return errWorkspaceInternal
	}
	return nil
}

func chownFileForTool(file *os.File) error {
	uid, gid, configured, err := configuredToolIdentity()
	if err != nil || !configured {
		return err
	}
	if os.Geteuid() != 0 {
		if os.Geteuid() == int(uid) && os.Getegid() == int(gid) {
			return nil
		}
		return fmt.Errorf("agentd must run as root to assign workspace ownership to uid %d", uid)
	}
	return file.Chown(int(uid), int(gid))
}

func createRootTemp(root *os.Root, dir, prefix string, mode os.FileMode) (*os.File, string, error) {
	for range 100 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := filepath.Join(dir, prefix+hex.EncodeToString(random[:]))
		file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", workspaceRootError(err)
		}
		if err := file.Chmod(mode); err != nil {
			file.Close()
			root.Remove(name)
			return nil, "", err
		}
		return file, name, nil
	}
	return nil, "", fmt.Errorf("create unique workspace file")
}

func (t *localTools) remove(name string) error {
	root, err := os.OpenRoot(t.root)
	if err != nil {
		return err
	}
	defer root.Close()
	return workspaceRootError(root.Remove(name))
}

func workspaceRootError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "escapes from parent") || strings.Contains(err.Error(), "outside root") {
		return fmt.Errorf("path escapes workspace: %w", err)
	}
	return err
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
