package experiment

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/helperregistry"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type dispatchCapture struct{ spec operation.Spec }

func (c *dispatchCapture) Submit(spec operation.Spec) operation.ID {
	c.spec = spec
	return "capture-operation"
}

func TestSubagentDispatcherDecodesPersistedOperationsAfterRecreation(t *testing.T) {
	manager, err := subagents.New(subagents.Config{Workspace: t.TempDir(), Runner: func(context.Context, runner.Options) (runner.RunResult, error) {
		return runner.RunResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	base := helperregistry.DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), manager)
	first, _, err := CompactSubagentTools(base)
	if err != nil {
		t.Fatal(err)
	}
	translator, _ := first.Resolve("Subagent")
	capture := &dispatchCapture{}
	status := translator.Translate(capture, llm.ToolCall{CallID: "persisted-call", Name: "Subagent", Arguments: `{"action":"wait","child_id":"child-1"}`})
	if status.Error != "" || len(status.WaitingFor) != 1 {
		t.Fatalf("translate status=%+v", status)
	}
	second, _, err := CompactSubagentTools(base)
	if err != nil {
		t.Fatal(err)
	}
	resumed, _ := second.Resolve("Subagent")
	for _, state := range []struct {
		name   string
		status operation.Status
		result string
		want   string
	}{
		{name: "running", status: operation.StatusReady, want: "Subagent operation is running."},
		{name: "completed", status: operation.StatusCompleted, result: "child report", want: "child report"},
	} {
		t.Run(state.name, func(t *testing.T) {
			spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: "pk.subagent.tool", Version: 1, Data: jsontext.Value(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			remoteState, err := operation.DecodeRemoteJobState(operation.Operation{ID: "persisted-op", Type: spec.Type, Version: spec.Version, Status: operation.StatusAwaiting, MaxOutputLength: spec.MaxOutputLength, State: spec.State})
			if err != nil {
				t.Fatal(err)
			}
			remoteState.TerminalResult = state.result
			encoded, err := json.Marshal(remoteState)
			if err != nil {
				t.Fatal(err)
			}
			result, err := resumed.TranslateResult("persisted-call", tool.CallStatus{}, []operation.Operation{{ID: "persisted-op", Type: spec.Type, Version: spec.Version, Status: state.status, MaxOutputLength: spec.MaxOutputLength, State: jsontext.Value(encoded)}})
			if err != nil || len(result.Output) != 1 || result.Output[0].Value != state.want {
				t.Fatalf("resumed result=%+v error=%v, want %q", result, err, state.want)
			}
		})
	}
}

func TestCompactSubagentToolsPreservesAllOperations(t *testing.T) {
	manager, err := subagents.New(subagents.Config{
		Workspace: t.TempDir(),
		Runner: func(context.Context, runner.Options) (runner.RunResult, error) {
			return runner.RunResult{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	base := helperregistry.DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), manager)
	registry, metrics, err := CompactSubagentTools(base)
	if err != nil {
		t.Fatal(err)
	}
	var expectedBefore int64
	for _, definition := range base.StaticDefinitions() {
		encoded, err := json.Marshal(definition.Tool)
		if err != nil {
			t.Fatal(err)
		}
		expectedBefore += int64(len(encoded))
	}
	if metrics.BeforeBytes != expectedBefore || metrics.AfterBytes <= 0 || metrics.AfterBytes*10 >= metrics.BeforeBytes*7 {
		t.Fatalf("unexpected serialized-size manipulation check: %+v", metrics)
	}
	t.Logf("serialized subagent definitions: before=%d after=%d saved=%d bytes", metrics.BeforeBytes, metrics.AfterBytes, metrics.BeforeBytes-metrics.AfterBytes)
	if got := len(registry.StaticDefinitions()); got != 1 {
		t.Fatalf("definition count=%d, want one dispatcher", got)
	}
	definitions := registry.StaticDefinitions()
	if definitions[0].Tool.Name != "Subagent" {
		t.Fatalf("dispatcher name=%q", definitions[0].Tool.Name)
	}
	if !strings.Contains(definitions[0].Tool.Description, "2 active") || !strings.Contains(definitions[0].Tool.Description, "8 queued") || !strings.Contains(definitions[0].Tool.Description, "not a sandbox") || !strings.Contains(definitions[0].Tool.Description, "parent's tools, write access, and OS permissions") {
		t.Fatalf("dispatcher description lost manager bounds or ownership guidance: %q", definitions[0].Tool.Description)
	}

	cases := []struct {
		action string
		args   string
		want   string
	}{
		{"start", `{"action":"start","task":"inspect","task_only":true}`, "start"},
		{"status", `{"action":"status","child_id":"child-1"}`, "status"},
		{"send", `{"action":"send","child_id":"child-1","text":"continue"}`, "send"},
		{"wait", `{"action":"wait","child_id":"child-1"}`, "wait"},
		{"cancel", `{"action":"cancel","child_id":"child-1"}`, "cancel"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			translator, ok := registry.Resolve("Subagent")
			if !ok {
				t.Fatal("dispatcher translator missing")
			}
			capture := &dispatchCapture{}
			status := translator.Translate(capture, llm.ToolCall{CallID: "call-" + tc.action, Name: "Subagent", Arguments: tc.args})
			if status.Error != "" || len(status.WaitingFor) != 1 {
				t.Fatalf("translation status=%+v", status)
			}
			var state operation.RemoteJobState
			if err := json.Unmarshal(capture.spec.State, &state); err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Request struct {
					Action string `json:"action"`
				} `json:"request"`
			}
			if err := json.Unmarshal(state.Plan.Data, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Request.Action != tc.want {
				t.Fatalf("delegated action=%q, want %q", payload.Request.Action, tc.want)
			}
			result, err := translator.TranslateResult("call-"+tc.action, tool.CallStatus{Error: "fixture result"}, nil)
			if err != nil || result.CallID != "call-"+tc.action || len(result.Output) != 1 || result.Output[0].Value != "fixture result" {
				t.Fatalf("delegated result=%+v error=%v", result, err)
			}
		})
	}
}

func TestSubagentDispatcherRejectsInvalidActionArguments(t *testing.T) {
	manager, err := subagents.New(subagents.Config{Workspace: t.TempDir(), Runner: func(context.Context, runner.Options) (runner.RunResult, error) {
		return runner.RunResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	base := helperregistry.DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), manager)
	registry, _, err := CompactSubagentTools(base)
	if err != nil {
		t.Fatal(err)
	}
	translator, _ := registry.Resolve("Subagent")
	cases := []struct{ name, args, want string }{
		{"unknown action", `{"action":"launch"}`, "action must be"},
		{"missing explicit task_only", `{"action":"start","task":"inspect"}`, "requires task and task_only"},
		{"blank task", `{"action":"start","task":" ","task_only":false,"files":["a.go"]}`, "task must be non-empty"},
		{"irrelevant field", `{"action":"wait","child_id":"c","text":"x"}`, "does not accept text"},
		{"missing child id", `{"action":"cancel"}`, "requires child_id"},
		{"blank steering", `{"action":"send","child_id":"c","text":" "}`, "requires child_id and non-empty text"},
		{"unknown key", `{"action":"wait","child_id":"c","extra":1}`, "invalid subagent tool arguments"},
		{"null field", `{"action":"wait","child_id":"c","text":null}`, "must not be null"},
		{"multiple values", `{"action":"wait","child_id":"c"}{}`, "expected one JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := translator.Translate(&dispatchCapture{}, llm.ToolCall{CallID: "invalid", Name: "Subagent", Arguments: tc.args})
			if !strings.Contains(status.Error, tc.want) {
				t.Fatalf("error=%q, want substring %q", status.Error, tc.want)
			}
			if len(status.WaitingFor) != 0 {
				t.Fatalf("invalid arguments submitted operations: %+v", status.WaitingFor)
			}
		})
	}
}
