package runner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderFingerprintIsPersistedAndGuardsResume(t *testing.T) {
	options := Options{Workspace: t.TempDir(), ProviderFingerprint: strings.Repeat("a", 64)}
	snapshot := newContextSnapshot(options, newToolRegistryBase(options.Workspace, options.Workspace), nil)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored ContextSnapshot
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ProviderFingerprint != options.ProviderFingerprint {
		t.Fatalf("provider fingerprint was not persisted: %q", restored.ProviderFingerprint)
	}
	if err := validateProviderFingerprint(restored, options.ProviderFingerprint); err != nil {
		t.Fatalf("matching provider rejected: %v", err)
	}
	if err := validateProviderFingerprint(restored, strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "start a new session") {
		t.Fatalf("changed provider was not rejected clearly: %v", err)
	}
}

func TestLoadSavedProviderForAttach(t *testing.T) {
	workspace := t.TempDir()
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	options := Options{Workspace: workspace, ProviderID: "team", ProviderFingerprint: strings.Repeat("c", 64)}
	snapshot := newContextSnapshot(options, newToolRegistryBase(workspace, workspace), nil)
	store := fileContextSnapshotStore{directory: sessionDir}
	if err := store.SaveContext(context.Background(), "session-a", snapshot); err != nil {
		t.Fatal(err)
	}
	id, fingerprint, saved, err := LoadSavedProvider(context.Background(), sessionDir, "session-a", workspace)
	if err != nil || !saved || id != "team" || fingerprint != options.ProviderFingerprint {
		t.Fatalf("saved provider=(%q,%q,%v,%v)", id, fingerprint, saved, err)
	}
	if _, _, saved, err := LoadSavedProvider(context.Background(), sessionDir, "missing", workspace); err != nil || saved {
		t.Fatalf("missing provider snapshot saved=%v err=%v", saved, err)
	}
}

func TestProviderFingerprintKeepsLegacyNativeSessionsResumable(t *testing.T) {
	legacy := ContextSnapshot{}
	if err := validateProviderFingerprint(legacy, ""); err != nil {
		t.Fatalf("legacy native session rejected: %v", err)
	}
	if err := validateProviderFingerprint(legacy, "configured-provider"); err == nil {
		t.Fatal("legacy session accepted a custom provider")
	}
}

func TestProviderIDPersistsAndPinsAttachSelection(t *testing.T) {
	options := Options{Workspace: t.TempDir(), ProviderID: "team"}
	snapshot := newContextSnapshot(options, newToolRegistryBase(options.Workspace, options.Workspace), nil)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored ContextSnapshot
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ProviderID != "team" {
		t.Fatalf("provider ID was not persisted: %q", restored.ProviderID)
	}
	if err := validateProviderSelection(restored, "team"); err != nil {
		t.Fatalf("matching provider rejected: %v", err)
	}
	if err := validateProviderSelection(restored, "other"); err == nil {
		t.Fatal("session allowed a different provider selection")
	}
}
