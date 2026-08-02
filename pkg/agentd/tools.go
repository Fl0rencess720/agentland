package agentd

import (
	"bufio"
	"bytes"
	"context"
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
	"syscall"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
)

type localTools struct {
	root   string
	skills *SkillRegistry
}

type shellInput struct {
	Command string `json:"command"`
	CWD     string `json:"cwd,omitempty"`
}

type shellOutput struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
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
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	t := &localTools{root: filepath.Clean(root), skills: skills}
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
			return nil, err
		}
		tools = append(tools, item)
	}
	return tools, nil
}

func (t *localTools) shell(ctx context.Context, input shellInput) (shellOutput, error) {
	if strings.TrimSpace(input.Command) == "" {
		return shellOutput{}, fmt.Errorf("command is required")
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	result := shellOutput{Output: output.String()}
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

func (t *localTools) readFile(_ context.Context, input pathInput) (string, error) {
	path, err := t.resolveExisting(input.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (t *localTools) writeFile(_ context.Context, input writeFileInput) (string, error) {
	path, err := t.resolveForWrite(input.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentd-write-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.WriteString(tmp, input.Content); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(input.Content), t.relative(path)), nil
}

func (t *localTools) editFile(_ context.Context, input editFileInput) (string, error) {
	if input.OldText == "" {
		return "", fmt.Errorf("old_text is required")
	}
	path, err := t.resolveExisting(input.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
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
	_, err = t.writeFile(context.Background(), writeFileInput{
		Path:    path,
		Content: strings.Replace(content, input.OldText, input.NewText, limit),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", count, t.relative(path)), nil
}

func (t *localTools) listFiles(_ context.Context, input pathInput) ([]string, error) {
	path := input.Path
	if path == "" {
		path = t.root
	}
	root, err := t.resolveExisting(path)
	if err != nil {
		return nil, err
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		files = append(files, t.relative(path))
		return nil
	})
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
		path = t.root
	}
	root, err := t.resolveExisting(path)
	if err != nil {
		return nil, err
	}
	var matches []grepMatch
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			if strings.IndexByte(text, 0) >= 0 {
				break
			}
			if pattern.MatchString(text) {
				matches = append(matches, grepMatch{Path: t.relative(path), Line: line, Text: text})
			}
		}
		_ = file.Close()
		return nil
	})
	return matches, err
}

func (t *localTools) readSkill(_ context.Context, input readSkillInput) (string, error) {
	if t.skills == nil {
		return "", fmt.Errorf("skills are unavailable")
	}
	return t.skills.Read(input.Name)
}

func (t *localTools) resolveExisting(path string) (string, error) {
	candidate, err := t.candidate(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !withinRoot(t.root, resolved) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return resolved, nil
}

func (t *localTools) resolveForWrite(path string) (string, error) {
	candidate, err := t.candidate(path)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(candidate)
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			if !withinRoot(t.root, resolved) {
				return "", fmt.Errorf("path escapes workspace")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) || parent == filepath.Dir(parent) {
			return "", err
		}
		parent = filepath.Dir(parent)
	}
	return candidate, nil
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

func (t *localTools) relative(path string) string {
	rel, err := filepath.Rel(t.root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
