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

	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
)

func TestRPCUpdateRequiresIdleAndStreamsOperationLifecycle(t *testing.T) {
	workspace := t.TempDir()
	sink := &rpcEventSink{events: make(chan []byte, 16)}
	started := make(chan struct{}, 1)
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, session: "session-update",
		opts: runner.Options{Workspace: workspace}, requestTypes: make(map[string]string)}
	server.runReleaseCommand = func(ctx context.Context, args []string, stdout, _ io.Writer) int {
		if len(args) != 3 || args[0] != "update" || args[1] != "--source" || args[2] != workspace {
			t.Errorf("update args=%q", args)
		}
		started <- struct{}{}
		_, _ = io.WriteString(stdout, "staging source\n")
		return 0
	}
	server.handle(rpcMessage{Version: 1, ID: "update-1", Type: "update", Payload: json.RawMessage(`{"source_path":"` + workspace + `"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "update_started" {
		t.Fatalf("first event=%s", event.Type)
	}
	if event := readRPCEvent(t, sink); event.Type != "update_progress" {
		t.Fatalf("second event=%s", event.Type)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("update command was not started")
	}
	if event := readRPCEvent(t, sink); event.Type != "update_progress" || !strings.Contains(event.Payload.(map[string]any)["text"].(string), "staging source") {
		t.Fatalf("progress event=%+v", event)
	}
	if event := readRPCEvent(t, sink); event.Type != "update_finished" || event.Payload.(map[string]any)["success"] != true {
		t.Fatalf("finish event=%+v", event)
	}
	server.mu.Lock()
	active := server.releaseActive
	server.mu.Unlock()
	if active {
		t.Fatal("update lifecycle stayed active after finish")
	}
}

func TestRPCUpdateDefaultDispatchReachesSourceValidation(t *testing.T) {
	t.Setenv("PK_LIB_DIR", filepath.Join(t.TempDir(), "install"))
	source := t.TempDir() // exists, but lacks the required pk source files
	payload, err := json.Marshal(map[string]string{"source_path": source})
	if err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 16)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true,
		opts: runner.Options{Workspace: t.TempDir()}, requestTypes: make(map[string]string)}
	server.handle(rpcMessage{Version: 1, ID: "real-update", Type: "update", Payload: payload}, make(chan turnDone, 1))
	validated := false
	for {
		event := readRPCEvent(t, sink)
		data, _ := event.Payload.(map[string]any)
		if event.Type == "update_progress" && data["stage"] == "source_validate" {
			validated = true
		}
		if event.Type == "update_finished" {
			message, _ := data["message"].(string)
			if !validated || data["success"] != false || !strings.Contains(message, "stage failed") || strings.Contains(message, "unexpected arguments") {
				t.Fatalf("default dispatch did not reach source validation: validated=%v payload=%v", validated, data)
			}
			break
		}
	}
}

func TestRPCUpdateCancelAndBusyGuard(t *testing.T) {
	workspace := t.TempDir()
	sink := &rpcEventSink{events: make(chan []byte, 16)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true,
		opts: runner.Options{Workspace: workspace}, requestTypes: make(map[string]string)}
	server.active = true
	server.handle(rpcMessage{Version: 1, ID: "update-busy", Type: "update"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" {
		t.Fatalf("busy update event=%s", event.Type)
	}
	server.mu.Lock()
	server.active = false
	server.mu.Unlock()
	started := make(chan struct{})
	server.runReleaseCommand = func(ctx context.Context, _ []string, _, _ io.Writer) int {
		close(started)
		<-ctx.Done()
		return 1
	}
	server.handle(rpcMessage{Version: 1, ID: "update-cancel", Type: "update", Payload: json.RawMessage(`{"source_path":"` + workspace + `"}`)}, make(chan turnDone, 1))
	readRPCEvent(t, sink) // update_started
	readRPCEvent(t, sink) // update_progress
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking update did not start")
	}
	server.handle(rpcMessage{Version: 1, ID: "cancel-release", Type: "update_cancel"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "update_cancel_requested" {
		t.Fatalf("cancel event=%s", event.Type)
	}
	if event := readRPCEvent(t, sink); event.Type != "update_finished" || event.Payload.(map[string]any)["success"] != false {
		t.Fatalf("canceled update finish=%+v", event)
	}
}

func TestRPCReloadRequiresIdleAndUsesValidatedHandoff(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_HOME", home)
	t.Setenv("PK_RELOAD_TOKEN", strings.Repeat("a", 64))
	t.Setenv("PK_RELOAD_SUPERVISOR_PID", "12345")
	sink := &rpcEventSink{events: make(chan []byte, 16)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, session: "resume-session", providerID: "fixture-provider",
		opts: runner.Options{Workspace: workspace, Model: "gpt-6-luna", Effort: "medium"}, requestTypes: make(map[string]string)}
	server.attachedTask = "task-1"
	server.handle(rpcMessage{Version: 1, ID: "reload-busy", Type: "reload"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" {
		t.Fatalf("attached task reload event=%s", event.Type)
	}
	server.attachedTask = ""
	server.handle(rpcMessage{Version: 1, ID: "reload-1", Type: "reload"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "reload_ready" {
		t.Fatalf("reload event=%+v", event)
	}
	path := filepath.Join(home, "reload", strings.Repeat("a", 64)+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("reload handoff file info=%v err=%v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var handoff reloadHandoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		t.Fatal(err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if handoff.SessionID != "resume-session" || handoff.ProviderID != "fixture-provider" || handoff.Workspace != resolvedWorkspace || handoff.Model != "gpt-6-luna" || handoff.Effort != "medium" {
		t.Fatalf("existing-session handoff=%+v err=%v", handoff, err)
	}
	server.handle(rpcMessage{Version: 1, ID: "reload-exit-1", Type: "reload_exit"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "reload_exit" {
		t.Fatalf("reload exit event=%s", event.Type)
	}
	if !server.quit {
		t.Fatal("reload_exit did not shut down RPC child")
	}
}

func TestRPCReloadSupportsFreshSessionAndNativeProviderSentinel(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_HOME", home)
	t.Setenv("PK_RELOAD_TOKEN", strings.Repeat("b", 64))
	t.Setenv("PK_RELOAD_SUPERVISOR_PID", "23456")
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(home, "config.json"), sessionDir: filepath.Join(home, "sessions"),
		started: true, opts: runner.Options{Workspace: workspace, Model: "custom-model", Effort: "low"}, requestTypes: make(map[string]string)}
	server.handle(rpcMessage{Version: 1, ID: "reload-fresh", Type: "reload"}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "reload_ready" || event.Payload.(map[string]any)["session_id"] != "" {
		t.Fatalf("fresh reload response=%+v", event)
	}
	data, err := os.ReadFile(filepath.Join(home, "reload", strings.Repeat("b", 64)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var handoff reloadHandoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		t.Fatal(err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if handoff.SessionID != "" || handoff.ProviderID != "native" || handoff.Workspace != resolvedWorkspace || handoff.Model != "custom-model" || handoff.Effort != "low" {
		t.Fatalf("fresh handoff=%+v", handoff)
	}
	server.handle(rpcMessage{Version: 1, ID: "reload-exit-fresh", Type: "reload_exit"}, make(chan turnDone, 1))
	if exit := readRPCEvent(t, sink); exit.Type != "reload_exit" || !server.quit {
		t.Fatalf("fresh reload exit=%+v quit=%v", exit, server.quit)
	}
}

func TestRPCStartNativeSentinelBypassesConfiguredProviderDefault(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_HOME", home)
	store := providers.Store{Home: home}
	if err := store.Put(providers.Provider{ID: "configured-default", Protocol: providers.ProtocolChatCompletions, BaseURL: "https://provider.example/v1", APIKeyEnv: "PK_MISSING_TEST_KEY", DefaultModel: "provider-model", DefaultEffort: "low"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefault("configured-default"); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(home, "config.json"), sessionDir: filepath.Join(home, "sessions"), requestTypes: make(map[string]string)}
	server.handle(rpcMessage{Version: 1, ID: "start-native", Type: "start", Payload: json.RawMessage(`{"workspace":"` + workspace + `","provider_id":"native","model":"gpt-6-luna","effort":"medium"}`)}, make(chan turnDone, 1))
	var ready rpcEvent
	for i := 0; i < 4; i++ {
		ready = readRPCEvent(t, sink)
		if ready.Type == "ready" || ready.Type == "error" {
			break
		}
	}
	if ready.Type != "ready" || ready.Payload.(map[string]any)["provider_id"] != "" {
		t.Fatalf("explicit native start did not bypass configured default: %+v", ready)
	}
}

func readRPCEvent(t *testing.T, sink *rpcEventSink) rpcEvent {
	t.Helper()
	select {
	case raw := <-sink.events:
		var event rpcEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RPC event")
		return rpcEvent{}
	}
}
