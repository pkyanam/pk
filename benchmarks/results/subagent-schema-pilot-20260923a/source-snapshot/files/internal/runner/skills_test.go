package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, path, name, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nUse this skill.\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNewToolRegistryDeduplicatesSameCanonicalSkillFile(t *testing.T) {
	root := t.TempDir()
	codexRoot := filepath.Join(root, ".codex", "skills")
	agentsRoot := filepath.Join(root, ".agents", "skills")
	actual := filepath.Join(root, "shared", "agentcalc", "SKILL.md")
	writeSkillFile(t, actual, "agentcalc", "Calculate with precision.")
	for _, root := range []string{codexRoot, agentsRoot} {
		link := filepath.Join(root, "agentcalc", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(actual, link); err != nil {
			t.Fatal(err)
		}
	}

	_, skills, warnings := newToolRegistry(root, filepath.Join(root, "operations"), []string{codexRoot, agentsRoot})
	if len(skills) != 1 || skills[0].Name != "agentcalc" {
		t.Fatalf("registered skills=%+v; expected one canonical skill", skills)
	}
	if len(warnings) != 0 {
		t.Fatalf("duplicate canonical file produced warnings: %v", warnings)
	}
}

func TestNewToolRegistryWarnsForSameNameFromDifferentFilesInStableOrder(t *testing.T) {
	root := t.TempDir()
	codexRoot := filepath.Join(root, ".codex", "skills")
	agentsRoot := filepath.Join(root, ".agents", "skills")
	codexSkill := filepath.Join(codexRoot, "agentcalc", "SKILL.md")
	agentsSkill := filepath.Join(agentsRoot, "agentcalc", "SKILL.md")
	writeSkillFile(t, codexSkill, "agentcalc", "First directory wins.")
	writeSkillFile(t, agentsSkill, "agentcalc", "This is a distinct file.")

	_, skills, warnings := newToolRegistry(root, filepath.Join(root, "operations"), []string{codexRoot, agentsRoot})
	if len(skills) != 1 || skills[0].Path != codexSkill {
		t.Fatalf("registered skills=%+v; expected first discovered skill %q", skills, codexSkill)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "already registered") {
		t.Fatalf("collision warnings=%v; expected one name-collision warning", warnings)
	}
}
