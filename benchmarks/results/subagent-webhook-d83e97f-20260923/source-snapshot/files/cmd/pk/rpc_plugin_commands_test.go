package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
)

func TestRPCPluginCommandCatalogAndExecution(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := buildRPCWorkspaceStatsWorker(t)
	manifestDir := t.TempDir()
	manifest := writeRPCPluginManifest(t, manifestDir, "workspace-stats", worker, "workspace_stats")
	sink := &rpcEventSink{events: make(chan []byte, 16)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard,
		started: true, pluginPaths: []string{manifest}, requestTypes: map[string]string{},
		opts: runner.Options{Workspace: workspace, SessionDir: sessions},
	}

	server.handle(rpcMessage{Version: 1, ID: "catalog", Type: "plugin_commands_list"}, make(chan turnDone, 1))
	catalog := readRPCEvent(t, sink)
	if catalog.Type != "plugin_commands" || catalog.ID != "catalog" {
		t.Fatalf("catalog event=%+v", catalog)
	}
	data, _ := json.Marshal(catalog.Payload)
	if !strings.Contains(string(data), "/ext:workspace-stats:stats") || !strings.Contains(string(data), "Show workspace file statistics") {
		t.Fatalf("catalog missing qualified command metadata: %s", data)
	}

	server.handle(rpcMessage{Version: 1, ID: "execute", Type: "plugin_command_execute", Payload: json.RawMessage(`{"name":"/ext:workspace-stats:stats","arguments":"ignored tail"}`)}, make(chan turnDone, 1))
	started := readRPCEvent(t, sink)
	if started.Type != "plugin_command_started" || started.ID != "execute" {
		t.Fatalf("command start event=%+v", started)
	}
	result := readRPCEvent(t, sink)
	if result.Type != "plugin_command_result" || result.ID != "execute" {
		t.Fatalf("command result event=%+v", result)
	}
	resultData, _ := json.Marshal(result.Payload)
	if !strings.Contains(string(resultData), "1 files") || !strings.Contains(string(resultData), "name") {
		t.Fatalf("command result payload=%s", resultData)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		server.mu.Lock()
		active := server.pluginCommandActive
		server.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plugin command remained active after result")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRPCPluginCommandRejectsConcurrentForegroundOperation(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 2)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, started: true, active: true, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "execute", Type: "plugin_command_execute", Payload: json.RawMessage(`{"name":"/ext:missing:cmd"}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "error" || !strings.Contains(event.Payload.(map[string]any)["message"].(string), "idle session") {
		t.Fatalf("busy command response=%+v", event)
	}
}

func TestRPCShutdownWaitsForPluginCommandTerminalEmission(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 2)}
	done := make(chan struct{})
	cancelCalled := make(chan struct{})
	server := &rpcServer{
		ctx: context.Background(), input: strings.NewReader(`{"version":1,"id":"quit","type":"shutdown"}` + "\n"),
		output: sink, diagnostics: io.Discard, requestTypes: map[string]string{}, pluginCommandActive: true,
		pluginCommandCancel: func() { close(cancelCalled) }, pluginCommandDone: done,
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.serve() }()
	if event := readRPCEvent(t, sink); event.Type != "shutdown" {
		t.Fatalf("shutdown response=%+v", event)
	}
	select {
	case <-cancelCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel plugin command")
	}
	select {
	case err := <-serveDone:
		t.Fatalf("serve returned before command terminal emission/cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(done)
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not finish after command cleanup")
	}
}
