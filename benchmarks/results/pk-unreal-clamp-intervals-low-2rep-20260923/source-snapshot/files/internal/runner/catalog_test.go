package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/session"
)

func writeCatalogSkill(t *testing.T, root, name, description, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + content
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSavedSkillCatalogAndReadUseFrozenSnapshot(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	skillRoot := filepath.Join(root, "skills")
	path := writeCatalogSkill(t, skillRoot, "review", "Review code carefully.", "old frozen text")
	snapshot := ContextSnapshot{
		Version:   contextSnapshotVersion,
		Workspace: workspace,
		Skills:    []ContextSkill{{Name: "review", Description: "Review code carefully.", Path: path, Content: []byte("old frozen text")}},
	}
	store := fileContextSnapshotStore{directory: sessionDir}
	if err := store.SaveContext(ctx, session.ID("catalog-session"), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed on disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadSkillCatalog(ctx, sessionDir, "catalog-session", []string{skillRoot}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Saved || len(catalog.Skills) != 1 || !catalog.Skills[0].Saved || catalog.Skills[0].Name != "review" {
		t.Fatalf("saved catalog = %#v", catalog)
	}
	read, err := ReadSkill(ctx, sessionDir, "catalog-session", []string{skillRoot}, workspace, "review")
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != "old frozen text" || !read.Skill.Saved {
		t.Fatalf("saved read = %#v, want frozen content", read)
	}
	if _, err := ReadSkill(ctx, sessionDir, "catalog-session", nil, workspace, "not-registered"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unknown saved skill error = %v", err)
	}
}

func TestFreshSkillCatalogReadsCurrentAndDeduplicatesCanonicalPath(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	workspace := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	path := writeCatalogSkill(t, skillRoot, "pk", "Built-in instructions.", "current text")
	aliasRoot := filepath.Join(root, "alias")
	if err := os.MkdirAll(aliasRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(path), filepath.Join(aliasRoot, "pk")); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadSkillCatalog(ctx, filepath.Join(root, "sessions"), "", []string{skillRoot, aliasRoot}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Saved || len(catalog.Skills) != 1 || catalog.Skills[0].Saved {
		t.Fatalf("fresh catalog = %#v", catalog)
	}
	read, err := ReadSkill(ctx, filepath.Join(root, "sessions"), "", []string{skillRoot}, workspace, "pk")
	if err != nil || !strings.Contains(read.Content, "current text") || read.Skill.Saved {
		t.Fatalf("fresh read = %#v, %v", read, err)
	}
	if !strings.Contains(catalog.Skills[0].Path, "SKILL.md") {
		t.Fatalf("catalog path = %q", catalog.Skills[0].Path)
	}
}

func TestFreshBundledSkillFlagMatchesCatalogAndDocument(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	skillRoot := filepath.Join(root, "bundled-skills", "build", "pk")
	path := writeCatalogSkill(t, skillRoot, "pk", "Bundled instructions.", "bundled content")
	catalog, err := LoadSkillCatalog(ctx, sessionDir, "", []string{skillRoot}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 1 || !catalog.Skills[0].Bundled || catalog.Skills[0].Path != path {
		t.Fatalf("fresh bundled catalog = %#v", catalog)
	}
	document, err := ReadSkill(ctx, sessionDir, "", []string{skillRoot}, t.TempDir(), "pk")
	if err != nil {
		t.Fatal(err)
	}
	if !document.Skill.Bundled || document.Skill.Path != path {
		t.Fatalf("fresh bundled document = %#v", document.Skill)
	}
}

func TestSkillCatalogHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := LoadSkillCatalog(ctx, t.TempDir(), "", nil, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadSkillCatalog() error = %v, want context.Canceled", err)
	}
	if _, err := ReadSkill(ctx, t.TempDir(), "", nil, t.TempDir(), "pk"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadSkill() error = %v, want context.Canceled", err)
	}
}
