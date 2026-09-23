package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestRunSubagentUsesInjectedProviderAndRunnerDefaults(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	var prepared, ran bool
	adapter := &mockModelAdapter{}
	cfg := subagentRuntimeConfig{
		UseCodex: true, CodexPath: "/explicit/auth.json", Diagnostics: io.Discard,
		AdapterFactory: func(ctx context.Context, useCodex bool, path string) (llm.Adapter, error) {
			if !useCodex || path != "/explicit/auth.json" {
				t.Fatalf("provider selection = (%v, %q)", useCodex, path)
			}
			prepared = true
			return adapter, nil
		},
		Run: func(ctx context.Context, options runner.Options) (runner.RunResult, error) {
			ran = true
			if options.Adapter != adapter || options.Model != "gpt-6-luna" || options.Effort != "low" || !options.JSONL || !options.ToolEvents {
				t.Fatalf("child runner options lost provider/model/event settings: %#v", options)
			}
			registry := tool.NewRegistry(tool.StaticTranslators{})
			if options.DecorateRegistry != nil {
				registry = options.DecorateRegistry(registry)
			}
			if _, ok := registry.Resolve("SubagentStart"); ok {
				t.Fatal("child unexpectedly received a nested subagent capability")
			}
			return runner.RunResult{SessionID: "child-session"}, nil
		},
	}
	result, err := runSubagent(context.Background(), runner.Options{Prompt: "task", Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared || !ran || result.SessionID != "child-session" || !adapter.closed {
		t.Fatalf("subagent provider/run/close = %v/%v/%v; result=%#v", prepared, ran, adapter.closed, result)
	}
}

type subagentScriptAdapter struct {
	mu       sync.Mutex
	calls    int
	closed   bool
	child    bool
	requests []llm.Request
}

func (a *subagentScriptAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.requests = append(a.requests, request)
	if a.child {
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Name, "Subagent") {
				return llm.Response{}, fmt.Errorf("nested tool %q exposed to child", tool.Name)
			}
		}
		return finalText("child completed"), nil
	}
	switch a.calls {
	case 1:
		return toolCall("start-call", "SubagentStart", `{"task":"Complete the child unit of work","files":["README.md"]}`), nil
	case 2:
		childID := findChildID(request)
		if childID == "" {
			return llm.Response{}, errors.New("SubagentStart result had no child ID")
		}
		args, _ := json.Marshal(map[string]string{"child_id": childID})
		return toolCall("wait-call", "SubagentWait", string(args)), nil
	default:
		return finalText("parent received child report"), nil
	}
}

func (a *subagentScriptAdapter) Close() error {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	return nil
}

func toolCall(id, name, args string) llm.Response {
	return llm.Response{ID: id, Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: name, Arguments: args}}}}
}

func finalText(text string) llm.Response {
	return llm.Response{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: text}}}}
}

func findChildID(request llm.Request) string {
	for _, item := range request.Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		result, ok := item.Data.(llm.ToolResult)
		if !ok || len(result.Output) == 0 {
			continue
		}
		var child struct {
			ID string `json:"ID"`
		}
		if json.Unmarshal([]byte(result.Output[0].Value), &child) == nil && child.ID != "" {
			return child.ID
		}
	}
	return ""
}

func TestParentRunnerCanStartAndWaitForChildWithoutNestedCapability(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	parentAdapter := &subagentScriptAdapter{}
	childAdapter := &subagentScriptAdapter{child: true}
	var eventsMu sync.Mutex
	var events []subagents.Event
	parent := runner.Options{Prompt: "delegate one file", Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low", Adapter: parentAdapter}
	manager, err := configureSubagents(context.Background(), &parent, subagentRuntimeConfig{
		Workspace: workspace, SessionDir: sessions, UseCodex: true, Diagnostics: io.Discard,
		Events: func(event subagents.Event) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		},
		AdapterFactory: func(context.Context, bool, string) (llm.Adapter, error) { return childAdapter, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := runner.Run(context.Background(), parent)
	manager.Close()
	if runErr != nil {
		t.Fatalf("parent run=%v", runErr)
	}
	if !strings.Contains(result.Text, "parent received child report") {
		t.Fatalf("parent result=%q", result.Text)
	}
	if childAdapter.calls != 1 || !childAdapter.closed {
		t.Fatalf("child calls=%d closed=%v", childAdapter.calls, childAdapter.closed)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	var sawChildFinal, sawCompletion bool
	for _, event := range events {
		if event.RequestID != "start-call" || event.ChildID == "" {
			continue
		}
		if event.Type == "assistant" && event.Phase == "final_answer" && strings.Contains(event.Text, "child completed") {
			sawChildFinal = true
		}
		if event.Type == "subagent" && event.State == "completed" {
			sawCompletion = true
		}
	}
	if !sawChildFinal || !sawCompletion {
		t.Fatalf("child event stream omitted final/completion: %#v", events)
	}
}

func TestRunSubagentInheritsExplicitMCPConfiguration(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	server := mcpclient.ServerConfig{ID: "fixture", Command: executable, Args: []string{"-test.run=^TestMain$"}, Env: map[string]string{"PK_TEST_MCP_SERVER": "1"}}
	adapter := &mockModelAdapter{}
	var found bool
	_, err = runSubagent(context.Background(), runner.Options{Prompt: "inspect", Workspace: t.TempDir(), SessionDir: t.TempDir(), Model: "gpt-6-luna", Effort: "low"}, subagentRuntimeConfig{
		InheritMCP: true, MCPServers: []mcpclient.ServerConfig{server}, Diagnostics: io.Discard,
		AdapterFactory: func(context.Context, bool, string) (llm.Adapter, error) { return adapter, nil },
		Run: func(_ context.Context, options runner.Options) (runner.RunResult, error) {
			if options.MCPFingerprint == "" || options.DecorateRegistry == nil || options.RemoteJobHandlers == nil {
				return runner.RunResult{}, errors.New("explicit MCP capability did not reach the child")
			}
			registry := options.DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}))
			for _, definition := range registry.StaticDefinitions() {
				if strings.HasPrefix(definition.Tool.Name, "mcp_fixture_") {
					found = true
				}
				if definition.Tool.Name == "SubagentStart" {
					return runner.RunResult{}, errors.New("child inherited nested spawn capability")
				}
			}
			return runner.RunResult{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found || !adapter.closed {
		t.Fatalf("MCP tool found=%v adapter closed=%v", found, adapter.closed)
	}
}
