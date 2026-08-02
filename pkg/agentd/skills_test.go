package agentd

import (
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

func writeSkill(t *testing.T, path, name, description, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
