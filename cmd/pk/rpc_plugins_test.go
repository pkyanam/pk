package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/extensions"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestRPCPluginSessionUsesFrozenManifestAndRejectsSchemaDrift(t *testing.T) {
	pkHome := t.TempDir()
	t.Setenv("PK_HOME", pkHome)
	workspace, sessions := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	workerPath := buildRPCWorkspaceStatsWorker(t)
	manifests := t.TempDir()
	primaryPath := writeRPCPluginManifest(t, manifests, "workspace-stats", workerPath, "workspace_stats")
	service := userPluginService()
	if _, err := service.Enable(primaryPath); err != nil {
		t.Fatal(err)
	}
	frozenPaths, issues, err := snapshotRPCPluginPaths(service)
	if err != nil || len(issues) != 0 || len(frozenPaths) != 1 {
		t.Fatalf("initial plugin snapshot paths=%v issues=%v err=%v", frozenPaths, issues, err)
	}
	// Updating config during a session affects the next session only.
	secondPath := writeRPCPluginManifest(t, manifests, "other-plugin", "/bin/echo", "other_tool")
	if _, err := service.Enable(secondPath); err != nil {
		t.Fatal(err)
	}
	currentPaths, issues, err := snapshotRPCPluginPaths(service)
	if err != nil || len(issues) != 0 || len(currentPaths) != 2 {
		t.Fatalf("updated plugin config paths=%v issues=%v err=%v", currentPaths, issues, err)
	}

	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "plugin-call", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "stats-call", Name: "workspace_stats", Arguments: `{}`}}}}},
		{response: llm.Response{ID: "plugin-result", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "The workspace has one file."}}}}},
	}}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(pkHome, "config.json"), sessionDir: sessions,
		started: true, pluginPaths: frozenPaths,
		opts:    runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter: adapter, requestTypes: map[string]string{},
	}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "plugin-prompt", Type: "prompt", Payload: json.RawMessage(`{"text":"count files"}`)}, finished)
	select {
	case result := <-finished:
		server.completeTurn(result)
		if result.err != nil {
			t.Fatalf("plugin prompt: %v", result.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("plugin prompt did not finish")
	}
	model.mu.Lock()
	requests := append([]llm.Request(nil), model.requests...)
	model.mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("model requests=%d; expected tool call and final response", len(requests))
	}
	toolNames := make(map[string]bool)
	for _, definition := range requests[0].Tools {
		toolNames[definition.Name] = true
	}
	if !toolNames["workspace_stats"] || toolNames["other_tool"] {
		t.Fatalf("current session tool schema changed after config update: %v", toolNames)
	}
	var foundResult bool
	for _, item := range requests[1].Input {
		if result, ok := item.Data.(llm.ToolResult); ok {
			for _, output := range result.Output {
				if strings.Contains(output.Value, "1 files") {
					foundResult = true
				}
			}
		}
	}
	if !foundResult {
		t.Fatalf("real extension result was not returned to model: %#v", requests[1].Input)
	}

	// A resumed session may not silently adopt an altered enabled plugin set.
	if err := service.Disable("workspace-stats"); err != nil {
		t.Fatal(err)
	}
	resumedPaths, _, err := snapshotRPCPluginPaths(service)
	if err != nil {
		t.Fatal(err)
	}
	server.pluginPaths = resumedPaths
	model.mu.Lock()
	before := model.calls
	model.mu.Unlock()
	server.handle(rpcMessage{Version: 1, ID: "plugin-resume", Type: "prompt", Payload: json.RawMessage(`{"text":"continue"}`)}, finished)
	select {
	case result := <-finished:
		server.completeTurn(result)
		if result.err == nil || !strings.Contains(result.err.Error(), "schemas differ") {
			t.Fatalf("resume schema drift error=%v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("schema drift validation did not finish")
	}
	model.mu.Lock()
	after := model.calls
	model.mu.Unlock()
	if after != before {
		t.Fatalf("changed plugin session reached model: calls %d -> %d", before, after)
	}
	_ = adapter.Close()
}

func TestRPCRelaysExtensionProgressWithPromptCorrelation(t *testing.T) {
	workspace := t.TempDir()
	manifest := extensions.Manifest{
		APIVersion: extensions.ProtocolVersion,
		ID:         "progress-fixture",
		Version:    "1.0.0",
		Executable: "fixture-worker",
		Tools: []extensions.ToolSpec{{
			Name:        "progress_tool",
			Description: "emit a progress update",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
	}
	host, report, err := extensions.NewHost(context.Background(), workspace, []extensions.Manifest{manifest}, func(context.Context, extensions.Manifest, string) (extensions.Worker, error) {
		return rpcProgressFixtureWorker{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Disabled) != 0 {
		t.Fatalf("extension disabled: %v", report.Disabled)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, requestTypes: map[string]string{}}
	stopProgress := relayRPCPluginProgress(context.Background(), server, host, "prompt-42")
	defer stopProgress()
	type toolOutcome struct {
		result extensions.ToolResult
		err    error
	}
	toolDone := make(chan toolOutcome, 1)
	go func() {
		result, err := host.ExecuteTool(context.Background(), "progress_tool", "call-7", json.RawMessage(`{}`))
		toolDone <- toolOutcome{result: result, err: err}
	}()
	var outcome toolOutcome
	select {
	case outcome = <-toolDone:
		if outcome.err != nil {
			t.Fatalf("execute progress tool: %v", outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("extension tool completion blocked on progress event delivery")
	}
	if len(outcome.result.Content) != 1 || outcome.result.Content[0].Text != "finished" {
		t.Fatalf("progress leaked into tool result: %+v", outcome.result)
	}
	select {
	case raw := <-sink.events:
		var event rpcEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode progress event: %v", err)
		}
		if event.Version != rpcVersion || event.ID != "prompt-42" || event.Type != "tool_progress" {
			t.Fatalf("unexpected progress envelope: %+v", event)
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok || payload["call_id"] != "call-7" || payload["name"] != "progress_tool" || payload["extension_id"] != "progress-fixture" || payload["text"] != "Scanning records" {
			t.Fatalf("unexpected progress payload: %#v", event.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("RPC did not relay extension progress")
	}
}

type rpcProgressFixtureWorker struct{}

func (rpcProgressFixtureWorker) Call(ctx context.Context, method string, params any, result any) error {
	switch method {
	case "initialize":
		var init extensions.InitializeParams
		encoded, _ := json.Marshal(params)
		if err := json.Unmarshal(encoded, &init); err != nil {
			return err
		}
		encoded, _ = json.Marshal(extensions.InitializeResult{APIVersion: extensions.ProtocolVersion, ID: init.ID, Tools: []string{"progress_tool"}})
		return json.Unmarshal(encoded, result)
	case "tool.execute":
		if !extensions.ReportProgress(ctx, "Scanning records") {
			return fmt.Errorf("progress event was not accepted")
		}
		encoded, _ := json.Marshal(extensions.ToolResult{Content: []extensions.Content{{Type: "text", Text: "finished"}}})
		return json.Unmarshal(encoded, result)
	default:
		return nil
	}
}

func (rpcProgressFixtureWorker) Close() error { return nil }

func buildRPCWorkspaceStatsWorker(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(t.TempDir(), "workspace-stats")
	command := exec.Command("go", "build", "-o", worker, "./internal/extensions/examples/workspace_stats")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build sample extension: %v\n%s", err, output)
	}
	return worker
}

func writeRPCPluginManifest(t *testing.T, dir, id, executable, toolName string) string {
	t.Helper()
	manifest := extensions.Manifest{
		APIVersion: extensions.ProtocolVersion, ID: id, Version: "1.0.0", Executable: executable,
		Tools: []extensions.ToolSpec{{Name: toolName, Description: "Inspect workspace files", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}},
	}
	if id == "workspace-stats" {
		manifest.Commands = []extensions.CommandSpec{{Name: "stats", Description: "Show workspace file statistics"}}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
