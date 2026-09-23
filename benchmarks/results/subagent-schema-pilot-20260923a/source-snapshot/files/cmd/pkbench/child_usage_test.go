package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOutputSeparatesDeduplicatedChildAndCombinedUsage(t *testing.T) {
	output := strings.Join([]string{
		`{"type":"assistant","response_id":"parent-1","text":"private assistant text"}`,
		`{"type":"usage","response_id":"parent-1","input_tokens":100,"output_tokens":20,"usage_available":true,"cached_input_tokens":10,"cached_input_tokens_available":true,"cache_write_input_tokens":2,"cache_write_input_tokens_available":true}`,
		`{"type":"tool_call","name":"SubagentStart","arguments_preview":"private args"}`,
		`{"type":"subagent_started","child_id":"child-a","task":"PRIVATE_TASK"}`,
		`{"type":"subagent_usage","child_id":"child-a","response_id":"child-r1","input_tokens":30,"output_tokens":7,"usage_available":true,"cached_input_tokens":4,"cached_input_tokens_available":true,"cache_write_input_tokens":1,"cache_write_input_tokens_available":true}`,
		`{"type":"subagent_usage","child_id":"child-a","response_id":"child-r1","input_tokens":30,"output_tokens":7,"usage_available":true,"cached_input_tokens":4,"cached_input_tokens_available":true,"cache_write_input_tokens":1,"cache_write_input_tokens_available":true}`,
	}, "\n")
	got := parseOutput("pk", []byte(output))
	if got.responses != 1 || got.input != 100 || got.output != 20 || got.cached != 10 {
		t.Fatalf("parent accounting was changed: responses=%d input=%d output=%d cached=%d", got.responses, got.input, got.output, got.cached)
	}
	if len(got.childResponses) != 1 || !got.subagentResponsesAvailable || got.subagentInput != 30 || got.subagentOutput != 7 || got.subagentCached != 4 || got.subagentWrites != 1 {
		t.Fatalf("child accounting = %#v", got)
	}
	if !got.subagentInputAvailable || !got.subagentOutputAvailable || !got.subagentCachedAvailable || !got.subagentWritesAvailable {
		t.Fatalf("child availability = input:%t output:%t cache:%t writes:%t", got.subagentInputAvailable, got.subagentOutputAvailable, got.subagentCachedAvailable, got.subagentWritesAvailable)
	}
	if !got.combinedResponsesAvailable || got.combinedResponses != 2 || got.combinedInput != 130 || got.combinedOutput != 27 || got.combinedCached != 14 || !got.combinedInputAvailable || !got.combinedOutputAvailable || !got.combinedCachedAvailable {
		t.Fatalf("combined accounting = %#v", got)
	}
	for _, event := range got.clean {
		if strings.Contains(stringValue(event["text"]), "private") || strings.Contains(stringValue(event["task"]), "PRIVATE") {
			t.Fatalf("child accounting caused private content to enter clean trace: %#v", event)
		}
	}
}

func TestParseOutputMarksUnknownAndPartialChildUsageUnavailable(t *testing.T) {
	output := strings.Join([]string{
		`{"type":"assistant","response_id":"parent-known","text":"ok"}`,
		`{"type":"usage","response_id":"parent-known","input_tokens":11,"output_tokens":3,"usage_available":true,"cached_input_tokens":0,"cached_input_tokens_available":true}`,
		`{"type":"assistant","response_id":"parent-unknown","text":"ok"}`,
		`{"type":"usage","response_id":"parent-unknown","usage_available":false}`,
		`{"type":"subagent_started","child_id":"child-a"}`,
		`{"type":"subagent_usage","child_id":"child-a","response_id":"child-known","input_tokens":50,"output_tokens":9,"usage_available":true,"cached_input_tokens":5,"cached_input_tokens_available":true}`,
		`{"type":"subagent_usage","child_id":"child-a","response_id":"child-unknown","usage_available":false}`,
	}, "\n")
	got := parseOutput("pk", []byte(output))
	if got.input != 11 || got.output != 3 || !got.inputAvailable || !got.outputAvailable {
		t.Fatalf("parent legacy totals/availability should preserve historical aggregation: %#v", got)
	}
	if got.subagentInputAvailable || got.subagentOutputAvailable || got.subagentCachedAvailable {
		t.Fatalf("partial child usage was presented as complete: %#v", got)
	}
	if got.subagentInput != 50 || got.subagentOutput != 9 || got.subagentCached != 5 {
		t.Fatalf("partial child observations not retained for diagnostics: %#v", got)
	}
	if !got.subagentResponsesAvailable || got.combinedResponses != 4 || !got.combinedResponsesAvailable {
		t.Fatalf("response counts should remain known despite missing token usage: %#v", got)
	}
	if got.combinedInputAvailable || got.combinedOutputAvailable || got.combinedCachedAvailable {
		t.Fatalf("combined usage must be unavailable when any response usage is unknown: %#v", got)
	}
}

func TestParseOutputMarksMissingChildUsageAndMalformedIdentityUnavailable(t *testing.T) {
	missing := strings.Join([]string{
		`{"type":"assistant","response_id":"p1","text":"ok"}`,
		`{"type":"usage","response_id":"p1","input_tokens":2,"output_tokens":1,"usage_available":true}`,
		`{"type":"tool_call","name":"SubagentStart"}`,
		`{"type":"subagent_started","child_id":"child-a"}`,
		`{"type":"subagent_response","child_id":"child-a","response_id":"child-r1"}`,
	}, "\n")
	got := parseOutput("pk", []byte(missing))
	if !got.subagentResponsesAvailable || got.subagentInputAvailable || got.combinedInputAvailable {
		t.Fatalf("response count should be known but token usage unavailable without usage marker: %#v", got)
	}
	if got.subagentInput != 0 {
		t.Fatalf("partial numeric value unexpectedly present: %d", got.subagentInput)
	}

	bad := `{"type":"subagent_usage","child_id":"child-a","response_id":"child-r2","input_tokens":-1,"output_tokens":1,"usage_available":true,"cached_input_tokens":0,"cached_input_tokens_available":true}`
	got = parseOutput("pk", []byte(bad))
	if got.subagentInputAvailable || got.combinedInputAvailable {
		t.Fatalf("negative child counter marked available: %#v", got)
	}
	if !got.subagentOutputAvailable || !got.subagentCachedAvailable || got.combinedCachedAvailable {
		t.Fatalf("per-metric availability flags were not kept independent: %#v", got)
	}
	validUsage := `{"type":"subagent_usage","child_id":"child-a","response_id":"child-r3","input_tokens":5,"output_tokens":2,"usage_available":true,"cached_input_tokens":1,"cached_input_tokens_available":true}`
	invalidMarker := `{"type":"subagent_accounting_unavailable","child_id":"child-a","response_id":"child-r3"}`
	for _, ordered := range [][]string{{validUsage, invalidMarker}, {invalidMarker, validUsage}} {
		got = parseOutput("pk", []byte(strings.Join(ordered, "\n")))
		if got.subagentInputAvailable || got.subagentOutputAvailable || got.subagentCachedAvailable {
			t.Fatalf("explicit unavailable marker failed to invalidate duplicate response in order %v: %#v", ordered, got)
		}
	}
}

func TestSubagentAccountingReportLabelsParentAndCombinedScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	record := runRecord{Engine: "pk", Task: "fixture", Phase: "implementation", ModelResponses: 2, InputTokens: 100, InputTokensAvailable: true, SubagentModelResponses: 1, SubagentResponsesAvailable: true, SubagentInputTokens: 30, SubagentInputAvailable: true, SubagentOutputTokens: 8, SubagentOutputAvailable: true, SubagentCachedTokens: 4, SubagentCachedAvailable: true, CombinedModelResponses: 3, CombinedResponsesAvailable: true, CombinedInputTokens: 130, CombinedInputAvailable: true, CombinedOutputTokens: 18, CombinedOutputAvailable: true, CombinedCachedTokens: 14, CombinedCachedAvailable: true}
	if err := writeMarkdown(path, suite{Records: []runRecord{record}}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"parent-runner only", "Subagent usage (separate accounting)", "Child input", "Combined input", "130"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("report missing %q:\n%s", required, content)
		}
	}
}

func TestDispatcherWithoutChildAccountingDoesNotImplyFreeDelegation(t *testing.T) {
	sum := parseOutput("pk", []byte(`{"type":"usage","response_id":"parent","usage_available":true,"input_tokens":10,"output_tokens":2}
{"type":"tool_call","name":"Subagent","state":"completed"}
`))
	if sum.subagentInputAvailable || sum.combinedInputAvailable {
		t.Fatal("dispatcher activity without child accounting was presented as complete")
	}
}
