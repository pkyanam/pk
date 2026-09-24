package runner

import (
	"context"
	"testing"
)

func TestPreviewToolCatalogIncludesWorkspaceDeltaWhenJournalEnabled(t *testing.T) {
	tools, _, err := PreviewToolCatalog(context.Background(), Options{
		Workspace: t.TempDir(), WorkspaceJournalRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tools {
		if item.Name == "WorkspaceDelta" && item.Source == "workspace journal" {
			return
		}
	}
	t.Fatalf("fresh journal-enabled preview omitted WorkspaceDelta: %#v", tools)
}

func TestPreviewToolCatalogOmitsWorkspaceDeltaWithoutJournal(t *testing.T) {
	tools, _, err := PreviewToolCatalog(context.Background(), Options{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tools {
		if item.Name == "WorkspaceDelta" {
			t.Fatalf("preview advertised WorkspaceDelta without journal support: %#v", tools)
		}
	}
}
