package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
)

func TestRPCSkillCatalogAndReadUseConfiguredDirectories(t *testing.T) {
	pkHome := t.TempDir()
	t.Setenv("PK_HOME", pkHome)
	workspace := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "skills")
	skillPath := filepath.Join(skillDir, "example", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: example\ndescription: example instructions\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &rpcServer{sessionDir: filepath.Join(pkHome, "sessions"), opts: runner.Options{Workspace: workspace, SkillsDirs: []string{skillDir}}}

	catalog, err := server.skillCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Saved || len(catalog.Skills) != 1 || catalog.Skills[0].Name != "example" {
		t.Fatalf("skill catalog = %#v", catalog)
	}
	document, err := server.readSkill(context.Background(), "example")
	if err != nil {
		t.Fatal(err)
	}
	if document.Skill.Name != "example" || document.Skill.Saved || document.Content == "" {
		t.Fatalf("skill document = %#v", document)
	}
	if _, err := server.readSkill(context.Background(), skillPath); err == nil {
		t.Fatal("readSkill accepted an arbitrary file path as a skill name")
	}
}
