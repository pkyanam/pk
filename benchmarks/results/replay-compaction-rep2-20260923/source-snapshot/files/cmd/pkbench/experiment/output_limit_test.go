package experiment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"uuid"
)

type fakeRegistry struct{ translator tool.Translator }

func (r fakeRegistry) StaticDefinitions() []tool.Definition {
	return []tool.Definition{{Tool: llm.Tool{Name: "Bash", Parameters: map[string]any{
		"properties": map[string]any{"max_output_length": map[string]any{"default": 40000, "description": "Defaults to 40000."}},
	}}}}
}

func TestFailedCommandIsRetainedPrivatelyForDiagnosis(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "failed.jsonl")
	capture := &captureTranslator{}
	counters := &Counters{}
	counters.CaptureFailedCommands(path)
	wrapped := Decorate(fakeRegistry{translator: capture}, 0, counters)
	translator, _ := wrapped.Resolve("Bash")
	const command = "git diff --check"
	translator.Translate(nil, llm.ToolCall{CallID: "call-1", Name: "Bash", Arguments: `{"command":"` + command + `"}`})
	_, err := translator.TranslateResult("call-1", tool.CallStatus{}, []operation.Operation{{Type: operation.TypeShell, Status: operation.StatusFailed}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("failed command log permissions = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]string
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["call_id"] != "call-1" || saved["command"] != command {
		t.Fatalf("saved failed command = %#v", saved)
	}
}
func (r fakeRegistry) Resolve(string) (tool.Translator, bool)                { return r.translator, true }
func (r fakeRegistry) RegisterSkill(tool.Skill) (tool.RegistrationID, error) { return uuid.Nil(), nil }
func (r fakeRegistry) UnregisterSkill(tool.RegistrationID)                   {}
func (r fakeRegistry) Skills() []tool.Skill                                  { return nil }

type captureTranslator struct{ args string }

func (t *captureTranslator) Translate(_ tool.Context, call llm.ToolCall) tool.CallStatus {
	t.args = call.Arguments
	return tool.CallStatus{}
}
func (*captureTranslator) TranslateResult(string, tool.CallStatus, []operation.Operation) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func TestDefaultOnlyAppliesToOmittedBashOutputLimit(t *testing.T) {
	for _, test := range []struct {
		name, input, wantLimit string
		mode                   int
		wantExplicit           int64
		wantOmitted            int64
		wantDefaulted          int64
	}{
		{name: "current omitted", input: `{"command":"true"}`, wantLimit: "", wantOmitted: 1},
		{name: "variant omitted", input: `{"command":"true"}`, wantLimit: `4096`, mode: 4096, wantOmitted: 1, wantDefaulted: 1},
		{name: "variant preserves explicit", input: `{"command":"true","max_output_length":17000}`, wantLimit: `17000`, mode: 4096, wantExplicit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := &captureTranslator{}
			counters := &Counters{}
			wrapped := Decorate(fakeRegistry{translator: capture}, test.mode, counters)
			translator, ok := wrapped.Resolve("Bash")
			if !ok {
				t.Fatal("Bash translator unavailable")
			}
			translator.Translate(nil, llm.ToolCall{Name: "Bash", Arguments: test.input})
			var got map[string]json.RawMessage
			if err := json.Unmarshal([]byte(capture.args), &got); err != nil {
				t.Fatal(err)
			}
			if test.wantLimit == "" {
				if _, exists := got["max_output_length"]; exists {
					t.Fatalf("current policy changed omitted arguments: %s", capture.args)
				}
			} else if string(got["max_output_length"]) != test.wantLimit {
				t.Fatalf("max_output_length = %s, want %s", got["max_output_length"], test.wantLimit)
			}
			if counts := counters.Snapshot(); counts != (Counts{Explicit: test.wantExplicit, Omitted: test.wantOmitted, Defaulted: test.wantDefaulted}) {
				t.Fatalf("counts = %#v", counts)
			}
		})
	}
}

func TestVariantAdvertisesItsAppliedDefault(t *testing.T) {
	defs := Decorate(fakeRegistry{}, 4096, &Counters{}).StaticDefinitions()
	properties := defs[0].Tool.Parameters["properties"].(map[string]any)
	limit := properties["max_output_length"].(map[string]any)
	if limit["default"] != 4096 {
		t.Fatalf("schema default = %v", limit["default"])
	}
	if limit["description"] != "Maximum characters per output text field. Truncated text keeps its head and tail, around a marker stating how much was omitted, and path to the file with the complete stream. Defaults to 4096." {
		t.Fatalf("schema description = %v", limit["description"])
	}
}
