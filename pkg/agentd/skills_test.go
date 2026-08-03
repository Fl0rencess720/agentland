package agentd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectSkillOverridesBuiltinSkill(t *testing.T) {
	workspace := t.TempDir()
	builtin := t.TempDir()
	writeSkill(t, filepath.Join(builtin, "demo", "SKILL.md"), "demo", "builtin", "builtin body")
	writeSkill(t, filepath.Join(workspace, ".agentland", "skills", "demo", "SKILL.md"), "demo", "project", "project body")

	registry, err := LoadSkills(builtin, workspace)
	require.NoError(t, err)
	require.Contains(t, registry.Index(), "demo: project")
	content, err := registry.Read("demo")
	require.NoError(t, err)
	require.Contains(t, content, "project body")
}

func TestSkillReadBoundsLargeBody(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".agentland", "skills", "large", "SKILL.md")
	writeSkill(t, path, "large", "large body", string(make([]byte, maxToolOutputBytes*2)))

	registry, err := LoadSkills("", workspace)
	require.NoError(t, err)
	content, err := registry.Read("large")
	require.NoError(t, err)
	require.LessOrEqual(t, len(content), maxToolOutputBytes)
	require.Contains(t, content, fmt.Sprintf("original size: %d bytes", fileSize(t, path)))
}

func TestSkillMetadataHasSizeLimit(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".agentland", "skills", "large", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	content := "---\nname: large\ndescription: " + string(make([]byte, maxSkillMetadataBytes)) + "\n---\nbody"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	_, err := LoadSkills("", workspace)
	require.ErrorContains(t, err, "unterminated YAML front matter")
}

func writeSkill(t *testing.T, path, name, description, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}
