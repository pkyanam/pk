package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/plugins"
)

func TestPluginSourceCommandPreviewInstallEnableAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	source := t.TempDir()
	component := filepath.Join(source, "extensions", "sample")
	if err := os.MkdirAll(component, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(source, "worker-ran")
	worker := "#!/bin/sh\nprintf ran > " + marker + "\n"
	workerPath := filepath.Join(component, "worker")
	if err := os.WriteFile(workerPath, []byte(worker), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"api_version":"pk.extensions/v1","id":"sample-plugin","version":"1.0.0","executable":"./worker","tools":[{"name":"sample_tool","description":"Test tool","parameters":{"type":"object","properties":{}}}]}`
	if err := os.WriteFile(filepath.Join(component, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	var preview bytes.Buffer
	if code := runPluginCommand(t.Context(), []string{"discover", source}, &preview, &preview); code != 0 {
		t.Fatalf("discover exit %d: %s", code, preview.String())
	}
	var catalog struct {
		Candidates []struct{ ID, ManifestPath string } `json:"candidates"`
	}
	if err := json.Unmarshal(preview.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Candidates) != 1 || catalog.Candidates[0].ID != "sample-plugin" {
		t.Fatalf("catalog=%+v", catalog)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("preview executed plugin worker: %v", err)
	}

	var added bytes.Buffer
	if code := runPluginCommand(t.Context(), []string{"add", "--id", "sample-plugin", source}, &added, &added); code != 0 {
		t.Fatalf("add exit %d: %s", code, added.String())
	}
	items, err := (plugins.Service{Home: home}).List()
	if err != nil || len(items) != 1 || items[0].ID != "sample-plugin" || !items[0].Enabled {
		t.Fatalf("plugins after add=%+v err=%v", items, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("install/enable executed plugin worker: %v", err)
	}

	var removed bytes.Buffer
	if code := runPluginCommand(t.Context(), []string{"remove", "sample-plugin"}, &removed, &removed); code != 0 {
		t.Fatalf("remove exit %d: %s", code, removed.String())
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "sample-plugin")); !os.IsNotExist(err) {
		t.Fatalf("managed package remains after remove: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("removed worker was executed: %v", err)
	}
	if !strings.Contains(removed.String(), `"plugins": []`) {
		t.Fatalf("remove response=%s", removed.String())
	}
}
