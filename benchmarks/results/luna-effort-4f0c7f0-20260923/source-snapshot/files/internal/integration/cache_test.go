package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type cacheProbeAdapter struct {
	requests []llm.Request
	options  []llm.RequestOptions
	response llm.Response
}

type extraDefinitionRegistry struct {
	tool.Registry
	definition llm.Tool
}

func (registry extraDefinitionRegistry) StaticDefinitions() []tool.Definition {
	definitions := registry.Registry.StaticDefinitions()
	return append(definitions, tool.Definition{Tool: registry.definition})
}

func (registry extraDefinitionRegistry) Resolve(name string) (tool.Translator, bool) {
	if name == registry.definition.Name {
		return extensionProbeTranslator{}, true
	}
	return registry.Registry.Resolve(name)
}

type extensionProbeTranslator struct{}

func (extensionProbeTranslator) Translate(tool.Context, llm.ToolCall) tool.CallStatus {
	return tool.CallStatus{}
}
func (extensionProbeTranslator) TranslateResult(callID string, _ tool.CallStatus, _ []operation.Operation) (llm.ToolResult, error) {
	return llm.ToolResult{CallID: callID}, nil
}

func TestRegistryDecoratorExtendsFreshSchemaButDoesNotRewriteSavedSchema(t *testing.T) {
	ctx := t.Context()
	workspace, sessionDir := t.TempDir(), t.TempDir()
	response := llm.Response{ID: "decorator-probe", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}
	first := &cacheProbeAdapter{response: response}
	firstTool := llm.Tool{Type: llm.ToolFunction, Name: "AskUser", Description: "Ask the user"}
	created, err := runner.Run(ctx, runner.Options{
		Prompt: "start", Workspace: workspace, SessionDir: sessionDir, Adapter: first,
		DecorateRegistry: func(registry tool.Registry) tool.Registry {
			return extraDefinitionRegistry{Registry: registry, definition: firstTool}
		},
	})
	if err != nil {
		t.Fatalf("initial Run() error = %v", err)
	}
	second := &cacheProbeAdapter{response: response}
	newTool := llm.Tool{Type: llm.ToolFunction, Name: "NewInteractionTool"}
	_, err = runner.Run(ctx, runner.Options{
		Prompt: "continue", SessionID: created.SessionID, Workspace: workspace, SessionDir: sessionDir, Adapter: second,
		DecorateRegistry: func(registry tool.Registry) tool.Registry {
			withSavedTool := extraDefinitionRegistry{Registry: registry, definition: firstTool}
			return extraDefinitionRegistry{Registry: withSavedTool, definition: newTool}
		},
	})
	if err != nil {
		t.Fatalf("resumed Run() error = %v", err)
	}
	if len(first.requests) != 1 || len(second.requests) != 1 {
		t.Fatalf("request counts = %d and %d, want one each", len(first.requests), len(second.requests))
	}
	toolNames := func(request llm.Request) []string {
		names := make([]string, 0, len(request.Tools))
		for _, definition := range request.Tools {
			names = append(names, definition.Name)
		}
		return names
	}
	firstNames, resumedNames := toolNames(first.requests[0]), toolNames(second.requests[0])
	if !reflect.DeepEqual(firstNames, resumedNames) {
		t.Fatalf("saved tool schema changed on resume: initial %v, resumed %v", firstNames, resumedNames)
	}
	if !containsString(firstNames, "AskUser") || containsString(resumedNames, "NewInteractionTool") {
		t.Fatalf("decorated schemas = initial %v, resumed %v; resume must retain saved AskUser schema", firstNames, resumedNames)
	}
}

func TestResumeRejectsMissingSavedToolBeforeModelRequest(t *testing.T) {
	ctx := t.Context()
	workspace, sessionDir := t.TempDir(), t.TempDir()
	response := llm.Response{ID: "decorator-probe", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}
	first := &cacheProbeAdapter{response: response}
	created, err := runner.Run(ctx, runner.Options{Prompt: "start", Workspace: workspace, SessionDir: sessionDir, Adapter: first,
		DecorateRegistry: func(registry tool.Registry) tool.Registry {
			return extraDefinitionRegistry{Registry: registry, definition: llm.Tool{Type: llm.ToolFunction, Name: "ImageGen"}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second := &cacheProbeAdapter{response: response}
	_, err = runner.Run(ctx, runner.Options{Prompt: "continue", SessionID: created.SessionID, Workspace: workspace, SessionDir: sessionDir, Adapter: second})
	if err == nil || !strings.Contains(err.Error(), "ImageGen") || !strings.Contains(err.Error(), "restore the original extension") {
		t.Fatalf("resume error = %v, want missing ImageGen diagnostic", err)
	}
	if len(second.requests) != 0 {
		t.Fatalf("provider was called %d times before rejecting missing tool", len(second.requests))
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (adapter *cacheProbeAdapter) Respond(_ context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	adapter.requests = append(adapter.requests, request)
	adapter.options = append(adapter.options, options)
	return adapter.response, nil
}

func TestResumeKeepsCachedPrefixStableAndReportsProviderUsage(t *testing.T) {
	ctx := t.Context()
	workspace, sessionDir := t.TempDir(), t.TempDir()
	agents := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("Keep the original project convention."), 0o600); err != nil {
		t.Fatal(err)
	}
	response := llm.Response{
		ID: "cache-probe", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}},
		Usage: llm.Usage{
			InputTokens: 1200, CachedInputTokens: 1024, CacheWriteInputTokens: 0,
			OutputTokens: 12,
			Raw:          jsontext.Value(`{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1024},"output_tokens":12}`),
		},
	}
	firstAdapter := &cacheProbeAdapter{response: response}
	var firstOutput strings.Builder
	created, err := runner.Run(ctx, runner.Options{
		Prompt: "start", Workspace: workspace, SessionDir: sessionDir,
		Adapter: firstAdapter, JSONL: true, Output: &firstOutput,
	})
	if err != nil {
		t.Fatalf("initial Run() error = %v", err)
	}

	if err := os.WriteFile(agents, []byte("Changed after the first turn."), 0o600); err != nil {
		t.Fatal(err)
	}
	secondAdapter := &cacheProbeAdapter{response: llm.Response{
		ID: "resume-probe", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "continued"}}},
	}}
	resumed, err := runner.Run(ctx, runner.Options{
		Prompt: "continue", SessionID: created.SessionID, Workspace: workspace, SessionDir: sessionDir,
		Model: "gpt-6-sol", Effort: "high", Adapter: secondAdapter,
	})
	if err != nil {
		t.Fatalf("resumed Run() error = %v", err)
	}
	if resumed.SessionID != created.SessionID {
		t.Fatalf("resumed session ID = %q, want %q", resumed.SessionID, created.SessionID)
	}
	if len(firstAdapter.requests) != 1 || len(secondAdapter.requests) != 1 {
		t.Fatalf("request counts = first %d, resumed %d; want one each", len(firstAdapter.requests), len(secondAdapter.requests))
	}
	if firstAdapter.requests[0].Model.ID != "gpt-6-luna" || firstAdapter.requests[0].Model.ReasoningEffort != llm.ReasoningEffortMedium {
		t.Errorf("default model settings = %#v, want gpt-6-luna/medium", firstAdapter.requests[0].Model)
	}
	if secondAdapter.requests[0].Model.ID != "gpt-6-sol" || secondAdapter.requests[0].Model.ReasoningEffort != llm.ReasoningEffortHigh {
		t.Errorf("resumed model settings = %#v, want requested gpt-6-sol/high", secondAdapter.requests[0].Model)
	}
	if firstAdapter.options[0].CacheKey != created.SessionID || secondAdapter.options[0].CacheKey != created.SessionID {
		t.Errorf("cache keys = %q and %q, want stable session ID %q", firstAdapter.options[0].CacheKey, secondAdapter.options[0].CacheKey, created.SessionID)
	}
	firstSystem := systemMessage(t, firstAdapter.requests[0])
	resumedSystem := systemMessage(t, secondAdapter.requests[0])
	lunaIdentity := fmt.Sprintf(pkIdentityTemplate, "gpt-6-luna")
	solIdentity := fmt.Sprintf(pkIdentityTemplate, "gpt-6-sol")
	if !strings.HasPrefix(firstSystem, lunaIdentity) || !strings.HasPrefix(resumedSystem, solIdentity) {
		t.Errorf("system identity does not follow selected models: first %q, resumed %q", firstSystem[:min(len(firstSystem), 140)], resumedSystem[:min(len(resumedSystem), 140)])
	}
	firstRemainder, firstFound := strings.CutPrefix(firstSystem, lunaIdentity)
	resumedRemainder, resumedFound := strings.CutPrefix(resumedSystem, solIdentity)
	if !firstFound || !resumedFound || firstRemainder != resumedRemainder {
		t.Errorf("system context changed beyond the selected-model identity")
	}
	if !strings.Contains(systemMessage(t, secondAdapter.requests[0]), "Keep the original project convention.") || strings.Contains(systemMessage(t, secondAdapter.requests[0]), "Changed after") {
		t.Error("resumed system prompt did not preserve the original AGENTS.md")
	}
	firstTools, err := json.Marshal(firstAdapter.requests[0].Tools)
	if err != nil {
		t.Fatal(err)
	}
	resumedTools, err := json.Marshal(secondAdapter.requests[0].Tools)
	if err != nil {
		t.Fatal(err)
	}
	var firstToolValue, resumedToolValue any
	if err := json.Unmarshal(firstTools, &firstToolValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resumedTools, &resumedToolValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstToolValue, resumedToolValue) {
		t.Errorf("tool definitions changed across resume\nfirst: %s\nresumed: %s", firstTools, resumedTools)
	}

	var usageEvent map[string]any
	for _, line := range strings.Split(strings.TrimSpace(firstOutput.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode JSONL event %q: %v", line, err)
		}
		if event["type"] == "usage" {
			usageEvent = event
		}
	}
	if usageEvent == nil {
		t.Fatal("JSONL output has no provider usage event")
	}
	if usageEvent["cached_input_tokens"] != float64(1024) || usageEvent["cached_input_tokens_available"] != true {
		t.Errorf("usage cache fields = %#v, want 1024 cached tokens available", usageEvent)
	}
	if usageEvent["cache_write_input_tokens_available"] != false {
		t.Errorf("cache-write availability = %#v, want false because provider omitted that field", usageEvent["cache_write_input_tokens_available"])
	}
}

func TestToolEventsReportRunningThenCompleted(t *testing.T) {
	arguments, err := json.Marshal(map[string]string{"command": "printf tool-finished"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{
		{ID: "tool-call", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call", Name: "Bash", Arguments: string(arguments)}}}},
		{ID: "answer", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}},
	}}
	var output strings.Builder
	if _, err := runner.Run(t.Context(), runner.Options{
		Prompt: "run a command", Workspace: t.TempDir(), SessionDir: t.TempDir(),
		Adapter: adapter, JSONL: true, Output: &output,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var states []string
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode JSONL event %q: %v", line, err)
		}
		if event["type"] == "tool_call" {
			if event["name"] != "Bash" {
				t.Errorf("tool name = %#v, want Bash", event["name"])
			}
			states = append(states, event["state"].(string))
		}
	}
	if !reflect.DeepEqual(states, []string{"running", "completed"}) {
		t.Fatalf("tool states = %#v, want running then completed", states)
	}
}

func TestMultiStepRunPreservesProgressAndStructuredToolUpdates(t *testing.T) {
	firstArgs, err := json.Marshal(map[string]string{"command": "API_KEY=super-secret printf first-out"})
	if err != nil {
		t.Fatal(err)
	}
	secondArgs, err := json.Marshal(map[string]string{"command": "printf second-out"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{
		{ID: "inspect", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "commentary", Text: "I’ll inspect the project and run a focused check."}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "first-call", Name: "Bash", Arguments: string(firstArgs)}},
		}},
		{ID: "verify", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "commentary", Text: "The first check passed; I’m verifying the follow-up now."}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "second-call", Name: "Bash", Arguments: string(secondArgs)}},
		}},
		{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "Both checks passed."}}}},
	}}
	var output strings.Builder
	if _, err := runner.Run(t.Context(), runner.Options{
		Prompt: "inspect and verify", Workspace: t.TempDir(), SessionDir: t.TempDir(),
		Adapter: adapter, JSONL: true, Output: &output,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode JSONL event %q: %v", line, err)
		}
		events = append(events, event)
	}
	var phases, assistantTexts []string
	var toolEvents []map[string]any
	for _, event := range events {
		switch event["type"] {
		case "assistant":
			phases = append(phases, event["phase"].(string))
			assistantTexts = append(assistantTexts, event["text"].(string))
		case "tool_call":
			toolEvents = append(toolEvents, event)
		}
	}
	if !reflect.DeepEqual(phases, []string{"commentary", "commentary", "final_answer"}) {
		t.Fatalf("assistant phases = %#v, want each visible progress message and final answer", phases)
	}
	if !reflect.DeepEqual(assistantTexts, []string{
		"I’ll inspect the project and run a focused check.",
		"The first check passed; I’m verifying the follow-up now.",
		"Both checks passed.",
	}) {
		t.Fatalf("assistant messages were lost or reordered: %#v", assistantTexts)
	}
	if len(toolEvents) != 4 {
		t.Fatalf("tool events = %d, want start and terminal update for both calls", len(toolEvents))
	}
	if toolEvents[0]["call_id"] != "first-call" || toolEvents[0]["state"] != "running" || toolEvents[1]["state"] != "completed" {
		t.Fatalf("first tool updates = %#v / %#v", toolEvents[0], toolEvents[1])
	}
	if toolEvents[0]["command_preview"] != "API_KEY=[redacted] printf first-out" {
		t.Errorf("redacted command preview = %#v", toolEvents[0]["command_preview"])
	}
	operations, ok := toolEvents[1]["operations"].([]any)
	if !ok || len(operations) != 1 {
		t.Fatalf("terminal tool operations = %#v", toolEvents[1]["operations"])
	}
	operation, ok := operations[0].(map[string]any)
	if !ok || operation["output_excerpt"] != "first-out" || operation["exit_code"] != float64(0) {
		t.Errorf("terminal shell result = %#v, want output excerpt and exit code", operations[0])
	}
	if strings.Contains(output.String(), "super-secret") {
		t.Fatal("JSONL tool previews exposed a secret assignment")
	}
	assistantIndex, firstToolIndex := -1, -1
	for index, event := range events {
		if event["type"] == "assistant" && event["phase"] == "commentary" && assistantIndex == -1 {
			assistantIndex = index
		}
		if event["type"] == "tool_call" && event["call_id"] == "first-call" && firstToolIndex == -1 {
			firstToolIndex = index
		}
	}
	if assistantIndex < 0 || firstToolIndex <= assistantIndex {
		t.Fatalf("progress update should precede tool status: assistant index %d, tool index %d", assistantIndex, firstToolIndex)
	}
}

func systemMessage(t *testing.T, request llm.Request) string {
	t.Helper()
	for _, item := range request.Input {
		if item.Type != llm.ItemMessage {
			continue
		}
		message, ok := item.Data.(llm.Message)
		if ok && message.Role == llm.RoleSystem {
			return message.Text
		}
	}
	t.Fatal("request has no system message")
	return ""
}
