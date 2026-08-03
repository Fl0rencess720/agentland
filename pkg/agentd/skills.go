package agentd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	maxSkills             = 256
	maxSkillMetadataBytes = 64 << 10
	maxSkillNameBytes     = 128
	maxSkillDescription   = 2048
)

type SkillRegistry struct {
	skills map[string]skill
}

type skill struct {
	Name        string
	Description string
	Path        string
}

type skillMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func LoadSkills(builtinDir, workspaceRoot string) (*SkillRegistry, error) {
	registry := &SkillRegistry{skills: make(map[string]skill)}
	for _, dir := range []string{builtinDir, filepath.Join(workspaceRoot, ".agentland", "skills")} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := registry.loadDir(dir); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *SkillRegistry) Index() string {
	if len(r.skills) == 0 {
		return ""
	}
	names := make([]string, 0, len(r.skills))
	for name := range r.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	out.WriteString("Available skills. Call read_skill when one is relevant:\n")
	for _, name := range names {
		item := r.skills[name]
		fmt.Fprintf(&out, "- %s: %s\n", item.Name, item.Description)
	}
	return strings.TrimSpace(out.String())
}

func (r *SkillRegistry) Read(name string) (string, error) {
	item, ok := r.skills[name]
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	file, err := os.Open(item.Path)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat skill %q: %w", name, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxToolOutputBytes+1))
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	originalSize := max(info.Size(), int64(len(data)))
	if len(data) > maxToolOutputBytes {
		data = data[:maxToolOutputBytes]
	}
	return boundToolOutputSize(string(data), originalSize), nil
}

func (r *SkillRegistry) loadDir(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() != "SKILL.md" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxSkillMetadataBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(data) > maxSkillMetadataBytes {
			data = data[:maxSkillMetadataBytes]
		}
		metadata, err := parseSkillMetadata(data)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if _, exists := r.skills[metadata.Name]; !exists && len(r.skills) >= maxSkills {
			return fmt.Errorf("skill count exceeds %d", maxSkills)
		}
		r.skills[metadata.Name] = skill{Name: metadata.Name, Description: metadata.Description, Path: path}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load skills from %s: %w", root, err)
	}
	return nil
}

func parseSkillMetadata(data []byte) (*skillMetadata, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("missing YAML front matter")
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated YAML front matter")
	}
	var metadata skillMetadata
	if err := yaml.Unmarshal([]byte(rest[:end]), &metadata); err != nil {
		return nil, err
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	if metadata.Name == "" || metadata.Description == "" {
		return nil, fmt.Errorf("name and description are required")
	}
	if len(metadata.Name) > maxSkillNameBytes || len(metadata.Description) > maxSkillDescription {
		return nil, fmt.Errorf("name or description is too long")
	}
	return &metadata, nil
}
