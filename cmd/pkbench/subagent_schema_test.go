package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubagentSchemaArmsCounterbalance(t *testing.T) {
	first, second := subagentSchemaArms(1), subagentSchemaArms(2)
	if first[0].mode != "subagent-schema-current" || first[1].mode != "compact-subagent-schema" || second[0].mode != first[1].mode || second[1].mode != first[0].mode {
		t.Fatalf("unexpected arm order: rep1=%v rep2=%v", first, second)
	}
}

func TestCopyImplementationFixtureOmitsHoldoutTests(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "impl.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "impl_test.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "workspace")
	if err := copyImplementationFixture(source, destination); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "impl.go"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Errorf("missing implementation input %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "impl_test.go")); !os.IsNotExist(err) {
		t.Fatalf("holdout test copied into model workspace: err=%v", err)
	}
}

func TestSubagentActionCoverageRequiresTwoCompletedMeasuredChildren(t *testing.T) {
	base := runRecord{SubagentStartedAvailable: true, SubagentStateAvailable: true, SubagentChildrenUsageAvail: true, SubagentInputAvailable: true, SubagentOutputAvailable: true, SubagentStartedChildren: 2, SubagentCompletedChildren: 2, SubagentChildrenWithUsage: 2}
	if !validSubagentActionCoverage(base) {
		t.Fatal("complete two-child run did not satisfy action coverage")
	}
	base.SubagentCompletedChildren = 1
	if validSubagentActionCoverage(base) {
		t.Fatal("failed/incomplete child was counted as action coverage")
	}
	base.SubagentCompletedChildren = 2
	base.SubagentInputAvailable = false
	if validSubagentActionCoverage(base) {
		t.Fatal("unknown usage was counted as action coverage")
	}
}

func TestParseSubagentSchemaActionCoverageAndMetrics(t *testing.T) {
	output := `{"type":"usage","response_id":"parent","usage_available":true,"input_tokens":10,"output_tokens":2}
{"type":"subagent_started","child_id":"a"}
{"type":"subagent_started","child_id":"b"}
{"type":"subagent_state","child_id":"a","state":"completed"}
{"type":"subagent_state","child_id":"b","state":"completed"}
{"type":"subagent_usage","child_id":"a","response_id":"a-r","usage_available":true,"input_tokens":5,"output_tokens":1}
{"type":"subagent_usage","child_id":"b","response_id":"b-r","usage_available":true,"input_tokens":6,"output_tokens":2}
{"type":"benchmark_subagent_schema","subagent_tool_definition_bytes_before":1000,"subagent_tool_definition_bytes_after":400,"subagent_tool_definition_bytes_saved":600,"subagent_schema_metrics_available":true}`
	got := parseOutput("pk-subagent-current", []byte(output))
	if got.subagentStartedChildren != 2 || got.subagentCompletedChildren != 2 || got.subagentChildrenWithUsage != 2 {
		t.Fatalf("child status/usage = %#v", got)
	}
	record := runRecord{SubagentStartedChildren: got.subagentStartedChildren, SubagentCompletedChildren: got.subagentCompletedChildren, SubagentChildrenWithUsage: got.subagentChildrenWithUsage, SubagentStartedAvailable: got.subagentStartedAvailable, SubagentStateAvailable: got.subagentStateAvailable, SubagentChildrenUsageAvail: got.subagentChildrenUsageAvailable, SubagentInputAvailable: got.subagentInputAvailable, SubagentOutputAvailable: got.subagentOutputAvailable}
	if !validSubagentActionCoverage(record) {
		t.Fatalf("valid child run did not count as action coverage: %#v", got)
	}
	if !got.subagentSchemaMetricsAvailable || got.subagentSchemaBytesBefore != 1000 || got.subagentSchemaBytesAfter != 400 || got.subagentSchemaBytesSaved != 600 {
		t.Fatalf("schema metrics = %#v", got)
	}
}

func TestSubagentTaskSelectionAndTimeouts(t *testing.T) {
	if got, err := parseSubagentTask(""); err != nil || got != "clamp+intervals" {
		t.Fatalf("default selection = %q, %v", got, err)
	}
	if got, err := parseSubagentTask("webhook"); err != nil || got != "webhook" {
		t.Fatalf("webhook selection = %q, %v", got, err)
	}
	if _, err := parseSubagentTask("unknown"); err == nil {
		t.Fatal("unknown task selection accepted")
	}
	if got := resolveSubagentWholeTimeout(20*time.Minute, false, 1); got != 15*time.Minute {
		t.Fatalf("single-repetition default timeout = %s", got)
	}
	if got := resolveSubagentWholeTimeout(20*time.Minute, false, 2); got != 30*time.Minute {
		t.Fatalf("paired-repetition default timeout = %s", got)
	}
	if got := resolveSubagentWholeTimeout(4*time.Minute, true, 2); got != 4*time.Minute {
		t.Fatalf("explicit timeout = %s", got)
	}
	for _, tc := range []struct {
		phase, whole time.Duration
		valid        bool
	}{{time.Minute, 15 * time.Minute, true}, {0, time.Minute, false}, {16 * time.Minute, time.Hour, false}, {time.Minute, 3 * time.Hour, false}} {
		if got := validSubagentTimeouts(tc.phase, tc.whole); got != tc.valid {
			t.Errorf("validSubagentTimeouts(%s,%s)=%t want %t", tc.phase, tc.whole, got, tc.valid)
		}
	}
}

func TestSubagentFixtureRoutingOmitsPristineHoldouts(t *testing.T) {
	repo := t.TempDir()
	for _, fixture := range []string{"webhook", "clamp", "intervals"} {
		dir := filepath.Join(repo, "benchmarks", "tasks", fixture)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"go.mod", "event.go", "signature.go", "memory_store.go", "handler.go", "webhook_test.go", "clamp.go", "intervals.go", "fixture_test.go"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("package fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	webhookWorkspace := filepath.Join(t.TempDir(), "webhook")
	if err := stageSubagentFixture(repo, webhookWorkspace, "webhook"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"event.go", "signature.go", "memory_store.go", "handler.go", "go.mod"} {
		if _, err := os.Stat(filepath.Join(webhookWorkspace, name)); err != nil {
			t.Errorf("webhook implementation input %s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(webhookWorkspace, "webhook_test.go")); !os.IsNotExist(err) {
		t.Fatalf("webhook holdout leaked into implementation workspace: %v", err)
	}

	defaultWorkspace := filepath.Join(t.TempDir(), "default")
	if err := stageSubagentFixture(repo, defaultWorkspace, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"clamp/clamp.go", "intervals/intervals.go"} {
		if _, err := os.Stat(filepath.Join(defaultWorkspace, name)); err != nil {
			t.Errorf("default implementation input %s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(defaultWorkspace, "clamp/fixture_test.go")); !os.IsNotExist(err) {
		t.Fatalf("default holdout leaked into implementation workspace: %v", err)
	}
}

func TestWebhookPromptHasDisjointTwoChildOwnership(t *testing.T) {
	prompt, err := subagentImplementationPrompt("webhook", "gpt-6-luna", "low")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"exactly two subagents", "event.go and signature.go", "memory_store.go", "You own and integrate handler.go", "Use model \"gpt-6-luna\" and reasoning effort \"low\""} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("webhook prompt missing %q", phrase)
		}
	}
}

func TestSubagentTaskCoverageRequiresExactlyTwoMatchingChildren(t *testing.T) {
	base := runRecord{
		SubagentStartedAvailable: true, SubagentStateAvailable: true, SubagentChildrenUsageAvail: true,
		SubagentInputAvailable: true, SubagentOutputAvailable: true,
		SubagentStartedChildren: 2, SubagentCompletedChildren: 2, SubagentChildrenWithUsage: 2,
		SubagentModelsAvailable: true, SubagentModels: []subagentModelRecord{{ChildID: "a", Model: "gpt-6-luna", Effort: "low"}, {ChildID: "b", Model: "gpt-6-luna", Effort: "low"}},
		CombinedResponsesAvailable: true, CombinedInputAvailable: true, CombinedOutputAvailable: true,
	}
	if !validSubagentTaskCoverage(base, "gpt-6-luna", "low") {
		t.Fatal("exactly two fully measured matching children did not satisfy coverage")
	}
	tooMany := base
	tooMany.SubagentStartedChildren = 3
	if validSubagentTaskCoverage(tooMany, "gpt-6-luna", "low") {
		t.Fatal("more than two children satisfied exact-action coverage")
	}
	wrongModel := base
	wrongModel.SubagentModels = append([]subagentModelRecord(nil), base.SubagentModels...)
	wrongModel.SubagentModels[1].Effort = "medium"
	if validSubagentTaskCoverage(wrongModel, "gpt-6-luna", "low") {
		t.Fatal("child with mismatched model or effort satisfied coverage")
	}
	partial := base
	partial.CombinedInputAvailable = false
	if validSubagentTaskCoverage(partial, "gpt-6-luna", "low") {
		t.Fatal("incomplete combined usage satisfied coverage")
	}
}

func TestSubagentTaskFlagRequiresSchemaExperimentAndRejectsUnknown(t *testing.T) {
	if code := run([]string{"-subagent-task", "webhook"}); code != 2 {
		t.Fatalf("task flag without experiment returned %d, want usage error 2", code)
	}
	if code := run([]string{"-subagent-schema-ablation", "-subagent-task", "unknown"}); code != 2 {
		t.Fatalf("unknown task returned %d, want usage error 2", code)
	}
}
