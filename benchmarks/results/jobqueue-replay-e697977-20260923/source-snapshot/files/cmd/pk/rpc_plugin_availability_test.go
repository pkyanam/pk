package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/plugins"
	"github.com/pkyanam/pk/internal/runner"
)

func TestRPCUnavailableEnabledPluginBlocksModelWithRecoveryAction(t *testing.T) {
	home, workspace, sessions := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PK_HOME", home)
	worker := filepath.Join(t.TempDir(), "worker")
	if err := os.WriteFile(worker, []byte("placeholder"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := writeRPCPluginManifest(t, t.TempDir(), "missing-worker", worker, "missing_tool")
	service := plugins.Service{Home: home}
	if _, err := service.Enable(manifest); err != nil {
		t.Fatalf("enable worker before removal: %v", err)
	}
	if err := os.Remove(worker); err != nil {
		t.Fatal(err)
	}
	paths, issues, err := snapshotRPCPluginPaths(service)
	if err != nil || len(paths) != 0 || len(issues) != 1 {
		t.Fatalf("snapshot paths=%v issues=%v err=%v", paths, issues, err)
	}
	items, err := service.List()
	if err != nil || len(items) != 1 || !strings.Contains(items[0].Error, "worker unavailable") {
		t.Fatalf("plugin catalog=%+v err=%v", items, err)
	}

	model := &mockModelAdapter{}
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		started: true, pluginIssues: pluginIssueMessages(issues),
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "prompt-broken-plugin", Type: "prompt", Payload: json.RawMessage(`{"text":"count files"}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	message := event.Payload.(map[string]any)["message"].(string)
	if event.Type != "error" || !strings.Contains(message, "/plugins") || !strings.Contains(message, "/new") {
		t.Fatalf("unavailable plugin event=%+v", event)
	}
	if model.calls != 0 {
		t.Fatalf("model was called %d times with an unavailable enabled plugin", model.calls)
	}
}
