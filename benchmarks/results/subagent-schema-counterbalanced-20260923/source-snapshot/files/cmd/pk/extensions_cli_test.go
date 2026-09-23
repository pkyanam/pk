package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/extensions"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type cliFakeExtensionWorker struct {
	mu        sync.Mutex
	toolRuns  int
	closed    bool
	lifecycle chan extensions.LifecycleEvent
}

func (worker *cliFakeExtensionWorker) Call(ctx context.Context, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch method {
	case "initialize":
		var init extensions.InitializeParams
		data, _ := json.Marshal(params)
		if err := json.Unmarshal(data, &init); err != nil {
			return err
		}
		value := extensions.InitializeResult{APIVersion: extensions.ProtocolVersion, ID: init.ID, Tools: []string{"fixture_info"}}
		if worker.lifecycle != nil {
			value.Features = []string{extensions.HostFeatureLifecycle}
		}
		data, _ = json.Marshal(value)
		return json.Unmarshal(data, result)
	case "lifecycle.notify":
		var event extensions.LifecycleEvent
		data, _ := json.Marshal(params)
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		worker.lifecycle <- event
		return nil
	case "tool.execute":
		worker.mu.Lock()
		worker.toolRuns++
		worker.mu.Unlock()
		data, _ := json.Marshal(extensions.ToolResult{Content: []extensions.Content{{Type: "text", Text: "extension fixture result"}}})
		return json.Unmarshal(data, result)
	default:
		return nil
	}
}

func (worker *cliFakeExtensionWorker) Close() error {
	worker.mu.Lock()
	worker.closed = true
	worker.mu.Unlock()
	return nil
}

func TestCLIExplicitExtensionDecoratesRunnerAndClosesWorker(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "fixture.json")
	manifest := extensions.Manifest{
		APIVersion: extensions.ProtocolVersion,
		ID:         "cli-fixture", Version: "1.0.0", Executable: "/fixture/worker",
		Hooks: []extensions.HookSpec{
			{Event: extensions.LifecycleRunStart, Mode: "observe"},
			{Event: extensions.LifecycleResponseComplete, Mode: "observe"},
			{Event: extensions.LifecycleRunEnd, Mode: "observe"},
		},
		Tools: []extensions.ToolSpec{{Name: "fixture_info", Description: "return fixture data", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &cliFakeExtensionWorker{lifecycle: make(chan extensions.LifecycleEvent, 4)}
	options := runner.Options{Prompt: "inspect", Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium", Output: io.Discard}
	var diagnostics strings.Builder
	host, err := configureCLIExtensions(context.Background(), &options, []string{manifestPath}, func(context.Context, extensions.Manifest, string) (extensions.Worker, error) {
		return worker, nil
	}, &diagnostics)
	if err != nil {
		t.Fatalf("configure extensions: %v", err)
	}
	if host == nil {
		t.Fatal("explicit extension host was not created")
	}
	args := `{"query":"example"}`
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "call-extension", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "fixture-call", Name: "fixture_info", Arguments: args}}}}},
		{response: llm.Response{ID: "finish", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "I got the extension result."}}}}},
	}}
	options.Adapter = model
	result, runErr := runner.Run(context.Background(), options)
	closeErr := host.Close()
	if runErr != nil {
		worker.mu.Lock()
		toolRuns := worker.toolRuns
		worker.mu.Unlock()
		t.Fatalf("runner with extension: %v; diagnostics=%q toolRuns=%d handlerFactory=%v", runErr, diagnostics.String(), toolRuns, options.RemoteJobHandlers != nil)
	}
	if closeErr != nil {
		t.Fatalf("close extension host: %v", closeErr)
	}
	wantLifecycleTypes := []string{"run_start", "response_complete", "response_complete", "run_end"}
	var lifecycleTypes []string
	for range wantLifecycleTypes {
		select {
		case event := <-worker.lifecycle:
			lifecycleTypes = append(lifecycleTypes, event.Type)
			if event.SessionID != result.SessionID || event.Model != "gpt-6-luna" || event.Workspace != workspace || event.RunID == "" {
				t.Errorf("unexpected lifecycle metadata: %+v", event)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for drained lifecycle notifications")
		}
	}
	if got, want := strings.Join(lifecycleTypes, ","), strings.Join(wantLifecycleTypes, ","); got != want {
		t.Fatalf("extension lifecycle events=%q, want %q", got, want)
	}
	if extra := len(worker.lifecycle); extra != 0 {
		t.Fatalf("unexpected extra lifecycle notifications: %d", extra)
	}
	if !strings.Contains(result.Text, "I got the extension result.") {
		t.Fatalf("assistant result=%q", result.Text)
	}
	worker.mu.Lock()
	toolRuns, closed := worker.toolRuns, worker.closed
	worker.mu.Unlock()
	if toolRuns != 1 || !closed {
		t.Fatalf("extension tool runs=%d closed=%v", toolRuns, closed)
	}
	if !strings.Contains(diagnostics.String(), "loaded extension cli-fixture") {
		t.Fatalf("diagnostics=%q", diagnostics.String())
	}
}

func TestCLIQuietExtensionSetupSuppressesOnlySuccessfulLoadNotice(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "fixture.json")
	manifest := extensions.Manifest{
		APIVersion: extensions.ProtocolVersion,
		ID:         "cli-fixture", Version: "1.0.0", Executable: "/fixture/worker",
		Tools: []extensions.ToolSpec{{Name: "fixture_info", Description: "return fixture data", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics strings.Builder
	host, err := configureCLIExtensionsQuiet(context.Background(), &runner.Options{Workspace: t.TempDir()}, []string{manifestPath}, func(context.Context, extensions.Manifest, string) (extensions.Worker, error) {
		return &cliFakeExtensionWorker{}, nil
	}, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if host == nil {
		t.Fatal("quiet setup did not load the explicitly configured extension")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics.String(), "loaded extension cli-fixture") {
		t.Fatalf("quiet setup repeated startup notice: %q", diagnostics.String())
	}
}
