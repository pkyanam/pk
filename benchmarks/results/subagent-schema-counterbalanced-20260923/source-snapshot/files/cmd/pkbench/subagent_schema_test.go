package main

import (
	"os"
	"path/filepath"
	"testing"
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
