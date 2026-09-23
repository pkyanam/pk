package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/imagegen"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestImageGenFingerprintIsPersistedAndGuardsResume(t *testing.T) {
	workspace := t.TempDir()
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	options := Options{Workspace: workspace, ImageGenFingerprint: "gpt-6-astra"}
	registry := imagegen.Decorator(imagegen.Config{Driver: "gpt-6-astra"}, workspace)(newToolRegistryBase(workspace, workspace))
	definitions := registry.StaticDefinitions()
	if len(definitions) == 0 || definitions[len(definitions)-1].Tool.Name != "ImageGen" {
		t.Fatal("configured fresh-session registry did not include ImageGen")
	}
	snapshot := newContextSnapshot(options, registry, nil)
	store := fileContextSnapshotStore{directory: sessionDir}
	if err := store.SaveContext(context.Background(), "image-session", snapshot); err != nil {
		t.Fatal(err)
	}
	fingerprint, saved, err := LoadSavedImageGenFingerprint(context.Background(), sessionDir, "image-session", workspace)
	if err != nil || !saved || fingerprint != options.ImageGenFingerprint {
		t.Fatalf("saved image driver=(%q,%v,%v)", fingerprint, saved, err)
	}
	if err := validateImageGenFingerprint(snapshot, "gpt-6-astra"); err != nil {
		t.Fatalf("matching driver rejected: %v", err)
	}
	for _, changed := range []string{"", "gpt-6-other"} {
		if err := validateImageGenFingerprint(snapshot, changed); err == nil || !strings.Contains(err.Error(), "start a new session") {
			t.Errorf("changed driver %q was not rejected clearly: %v", changed, err)
		}
	}
}

func TestImageGenFingerprintDoesNotChangeLegacyNonImageSessions(t *testing.T) {
	legacy := ContextSnapshot{Tools: []llm.Tool{{Name: "Bash"}}}
	if err := validateImageGenFingerprint(legacy, "gpt-6-astra"); err != nil {
		t.Fatalf("legacy session without ImageGen rejected: %v", err)
	}
	unknown := ContextSnapshot{Tools: []llm.Tool{{Name: "ImageGen"}}}
	if err := validateImageGenFingerprint(unknown, "gpt-6-astra"); err == nil || !strings.Contains(err.Error(), "cannot be resumed safely") {
		t.Fatalf("unknown legacy ImageGen driver did not fail clearly: %v", err)
	}
}
