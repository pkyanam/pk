package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestMaterializedSkillIsDiscoverableAndRepairsChangedCopy(t *testing.T) {
	home := t.TempDir()
	directory, err := Materialize(home)
	if err != nil {
		t.Fatal(err)
	}
	discovered, warnings := tool.DiscoverSkills(directory)
	if len(warnings) != 0 || len(discovered) != 1 || discovered[0].Name != "pk" {
		t.Fatalf("skills=%v warnings=%v", discovered, warnings)
	}
	path := filepath.Join(directory, "pk", "SKILL.md")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := Materialize(home)
	if err != nil || again != directory {
		t.Fatalf("directory=%q err=%v", again, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != string(pkSkill) {
		t.Fatalf("embedded copy not restored: %v", err)
	}
}
