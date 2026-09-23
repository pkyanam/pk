package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
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

func TestFreshCatalogUsesYAMLDecodedSkillDescription(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	skillDir := filepath.Join(skillRoot, "monid-search")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: monid-search\ndescription: >-\n  Search public sources and\n  cite the resulting pages.\n---\nInstructions.\n"
	path := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadSkillCatalog(t.Context(), filepath.Join(root, "sessions"), "", []string{skillRoot}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Description != "Search public sources and cite the resulting pages." {
		t.Fatalf("catalog did not decode folded description: %#v", catalog)
	}
	document, err := ReadSkill(t.Context(), filepath.Join(root, "sessions"), "", []string{skillRoot}, workspace, "monid-search")
	if err != nil || document.Skill.Description != catalog.Skills[0].Description || document.Content != content {
		t.Fatalf("read document metadata/content = %#v, %v", document, err)
	}
}

type skillDescriptionCapture struct{ input []byte }

func (a *skillDescriptionCapture) Respond(_ context.Context, req llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.input, _ = json.Marshal(req.Input)
	return llm.Response{ID: "skill-metadata", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "done"}}}}, nil
}

func TestRunUsesDecodedSkillDescriptionAndResumeKeepsSnapshot(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	skillRoot := filepath.Join(root, "skills")
	skillDir := filepath.Join(skillRoot, "monid-search")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	content := "---\nname: monid-search\ndescription: >-\n  Search sources and\n  cite results.\n---\nInstructions.\n"
	if err := os.WriteFile(skillPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	firstAdapter := &skillDescriptionCapture{}
	result, err := Run(t.Context(), Options{Prompt: "inspect the search skill", Workspace: workspace, SessionDir: sessionDir, SkillsDirs: []string{skillRoot}, Adapter: firstAdapter})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firstAdapter.input), "Search sources and cite results.") {
		t.Fatalf("fresh model request did not use decoded description: %s", firstAdapter.input)
	}
	store := defaultContextSnapshotStore(sessionDir, workspace)
	snapshot, err := store.LoadContext(t.Context(), session.ID(result.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Skills) != 1 || snapshot.Skills[0].Description != "Search sources and cite results." {
		t.Fatalf("saved context skill metadata = %#v", snapshot.Skills)
	}
	resumeAdapter := &skillDescriptionCapture{}
	if _, err := Run(t.Context(), Options{Prompt: "continue", SessionID: result.SessionID, Workspace: workspace, SessionDir: sessionDir, SkillsDirs: []string{skillRoot}, Adapter: resumeAdapter}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resumeAdapter.input), "Search sources and cite results.") {
		t.Fatalf("resumed model request lost saved description: %s", resumeAdapter.input)
	}
}

func TestRunRegistersQuotedSkillNameAndSkipsMalformedOptionalMetadata(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	skillRoot := filepath.Join(root, "skills")
	validDir := filepath.Join(skillRoot, "quoted")
	badDir := filepath.Join(skillRoot, "duplicate")
	for _, dir := range []string{validDir, badDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	valid := "---\nname: \"quoted-name\"\ndescription: >-\n  A valid quoted-name skill\n  with a folded summary.\n---\nInstructions.\n"
	malformed := "---\nname: discarded\nname: duplicate-key\ndescription: Invalid optional skill.\n---\nDo not load.\n"
	if err := os.WriteFile(filepath.Join(validDir, "SKILL.md"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	adapter := &skillDescriptionCapture{}
	result, err := Run(t.Context(), Options{Prompt: "use the skill", Workspace: workspace, SessionDir: sessionDir, SkillsDirs: []string{skillRoot}, Diagnostics: &diagnostics, Adapter: adapter})
	if err != nil {
		t.Fatalf("optional malformed skill should not fail the session: %v", err)
	}
	if !strings.Contains(string(adapter.input), "quoted-name") || !strings.Contains(string(adapter.input), "A valid quoted-name skill with a folded summary.") || strings.Contains(string(adapter.input), "duplicate-key") {
		t.Fatalf("model request skills did not match valid parsed catalog: %s", adapter.input)
	}
	if !strings.Contains(diagnostics.String(), "duplicate name field") {
		t.Fatalf("malformed optional skill warning missing: %s", diagnostics.String())
	}
	snapshot, err := defaultContextSnapshotStore(sessionDir, workspace).LoadContext(t.Context(), session.ID(result.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Skills) != 1 || snapshot.Skills[0].Name != "quoted-name" || snapshot.Skills[0].Description != "A valid quoted-name skill with a folded summary." {
		t.Fatalf("snapshot skills = %#v", snapshot.Skills)
	}
	catalog, err := LoadSkillCatalog(t.Context(), sessionDir, "", []string{skillRoot}, workspace)
	if err != nil || len(catalog.Skills) != 1 || catalog.Skills[0].Name != snapshot.Skills[0].Name || catalog.Skills[0].Description != snapshot.Skills[0].Description {
		t.Fatalf("catalog = %#v, %v", catalog, err)
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
