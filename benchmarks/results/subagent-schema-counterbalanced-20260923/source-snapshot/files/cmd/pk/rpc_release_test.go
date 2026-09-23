package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeReleaseManifest(t *testing.T, directory, id string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"id": id, "path": directory})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "release.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRPCReleaseStatusDetectsManagedReleaseMismatch(t *testing.T) {
	root := t.TempDir()
	running := filepath.Join(root, "running", "releases", "old")
	installed := filepath.Join(root, "library", "releases", "new")
	writeReleaseManifest(t, running, "old")
	writeReleaseManifest(t, installed, "new")
	if err := os.Symlink(filepath.Join("releases", "new"), filepath.Join(root, "library", "current")); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(running, "pk")
	status := rpcReleaseStatusFor(func() (string, error) { return executable, nil }, func() string { return filepath.Join(root, "library") })
	if status.RunningID != "old" || status.InstalledID != "new" || !status.ReloadAvailable {
		t.Fatalf("release status = %+v", status)
	}
}

func TestRPCReleaseStatusDoesNotRecommendUnknownOrEqualRelease(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "library", "releases", "same")
	writeReleaseManifest(t, installed, "same")
	if err := os.Symlink(filepath.Join("releases", "same"), filepath.Join(root, "library", "current")); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(installed, "pk")
	status := rpcReleaseStatusFor(func() (string, error) { return executable, nil }, func() string { return filepath.Join(root, "library") })
	if status.RunningID != "same" || status.InstalledID != "same" || status.ReloadAvailable {
		t.Fatalf("equal releases status = %+v", status)
	}
	status = rpcReleaseStatusFor(func() (string, error) { return filepath.Join(root, "unmanaged", "pk"), nil }, func() string { return filepath.Join(root, "library") })
	if status.RunningID != "" || status.InstalledID != "same" || status.ReloadAvailable {
		t.Fatalf("unmanaged executable status = %+v", status)
	}
}
