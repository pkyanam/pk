package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
	pluginconfig "github.com/pkyanam/pk/internal/plugins"
	"github.com/pkyanam/pk/internal/runner"
)

func TestSnapshotRPCPluginPathsSkipsInvalidEntry(t *testing.T) {
	home := t.TempDir()
	manifest := extensions.Manifest{APIVersion: extensions.ProtocolVersion, ID: "plugin_one", Version: "1.0.0", Executable: "/bin/echo"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "plugin.json")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	service := pluginconfig.Service{Home: home}
	if _, err := service.Enable(manifestPath); err != nil {
		t.Fatal(err)
	}
	paths, diagnostics, err := snapshotRPCPluginPaths(service)
	if err != nil || len(diagnostics) != 0 || len(paths) != 1 {
		t.Fatalf("paths=%v diagnostics=%v err=%v", paths, diagnostics, err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	paths, diagnostics, err = snapshotRPCPluginPaths(service)
	if err != nil || len(paths) != 0 || len(diagnostics) != 1 {
		t.Fatalf("invalid enabled plugin handling paths=%v diagnostics=%v err=%v", paths, diagnostics, err)
	}
}

func TestPluginSessionFingerprintRejectsSchemaDrift(t *testing.T) {
	sessionDir := t.TempDir()
	if err := checkPluginSessionFingerprint(sessionDir, "session-1", ""); err != nil {
		t.Fatalf("legacy no-plugin session: %v", err)
	}
	if err := checkPluginSessionFingerprint(sessionDir, "session-1", "schema-one"); err == nil {
		t.Fatal("plugin-enabled resume without a saved schema was accepted")
	}
	if err := savePluginSessionFingerprint(sessionDir, "session-1", "schema-one"); err != nil {
		t.Fatal(err)
	}
	path := pluginSessionFingerprintPath(sessionDir, "session-1")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("fingerprint sidecar permissions=%v err=%v", info, err)
	}
	if err := checkPluginSessionFingerprint(sessionDir, "session-1", "schema-one"); err != nil {
		t.Fatal(err)
	}
	if err := checkPluginSessionFingerprint(sessionDir, "session-1", "schema-two"); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("schema drift error=%v", err)
	}
	if err := checkPluginSessionFingerprint(sessionDir, "session-2", "schema-one"); err == nil {
		t.Fatal("sidecar from a different session was accepted")
	}
}

func TestRPCPluginMutationsMarkNextSessionOnly(t *testing.T) {
	service := pluginconfig.Service{Home: t.TempDir()}
	manifest := extensions.Manifest{APIVersion: extensions.ProtocolVersion, ID: "plugin_one", Version: "1.0.0", Executable: "/bin/echo"}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plugin.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := enableRPCPlugin(service, path)
	if err != nil {
		t.Fatal(err)
	}
	if payload["next_session_only"] != true {
		t.Fatalf("enable response %v", payload)
	}
	payload, err = disableRPCPlugin(service, "plugin_one")
	if err != nil || payload["next_session_only"] != true {
		t.Fatalf("disable response %v err=%v", payload, err)
	}
}

func TestNormalizePluginManifestPathUsesWorkspaceAndHandlesHome(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := normalizePluginManifestPath("plugins/example manifest.json", workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workspace, "plugins", "example manifest.json")
	if got != want {
		t.Fatalf("relative manifest path = %q, want %q", got, want)
	}
	t.Setenv("HOME", workspace)
	got, err = normalizePluginManifestPath("~/plugins/example manifest.json", "")
	if err != nil || got != want {
		t.Fatalf("home manifest path = %q, err=%v, want %q", got, err, want)
	}
	if _, err := normalizePluginManifestPath("~another-user/manifest.json", workspace); err == nil {
		t.Fatal("named-user expansion accepted")
	}
}

func TestPrepareRPCPluginSessionPersistsAndChecksFrozenSchema(t *testing.T) {
	sessionDir := t.TempDir()
	called := ""
	options := runner.Options{SessionDir: sessionDir, OnSession: func(id string) { called = id }}
	finish, err := prepareRPCPluginSession(&options, nil)
	if err != nil {
		t.Fatal(err)
	}
	options.OnSession("fresh-session")
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if called != "fresh-session" {
		t.Fatalf("prior OnSession callback not preserved: %q", called)
	}
	resume := runner.Options{SessionDir: sessionDir, SessionID: "fresh-session"}
	if _, err := prepareRPCPluginSession(&resume, nil); err != nil {
		t.Fatalf("matching plugin schema was rejected: %v", err)
	}
	if err := checkPluginSessionFingerprint(sessionDir, "fresh-session", "different"); err == nil {
		t.Fatal("changed plugin schema was accepted on resume")
	}
}
