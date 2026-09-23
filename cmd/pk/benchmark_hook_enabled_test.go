//go:build pkbench

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
)

func TestReplayOverrideEnvironmentIsScopedToReplayPolicies(t *testing.T) {
	t.Setenv("PK_BENCH_REPLAY_THRESHOLD_BYTES", "invalid")
	t.Setenv("PK_BENCH_REPLAY_EXCERPT_RUNES", "invalid")
	t.Setenv("PK_BENCH_POLICY", "current")
	if _, err := beginBenchmarkRun(&runner.Options{}); err != nil {
		t.Fatalf("unrelated policy should ignore replay overrides: %v", err)
	}
	t.Setenv("PK_BENCH_POLICY", "replay-compaction-current")
	if _, err := beginBenchmarkRun(&runner.Options{}); err == nil {
		t.Fatal("replay policy should reject malformed replay override")
	}
}

func TestLargeReplayProfileEmitsExactManipulationMetadata(t *testing.T) {
	t.Setenv("PK_BENCH_POLICY", "compact-replayed-shell-output-large")
	t.Setenv("PK_BENCH_REPLAY_THRESHOLD_BYTES", "16384")
	t.Setenv("PK_BENCH_REPLAY_EXCERPT_RUNES", "2048")
	var output bytes.Buffer
	options := runner.Options{JSONL: true, Output: &output}
	finish, err := beginBenchmarkRun(&options)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	for _, expected := range []string{`"mode":"compact-replayed-shell-output-large"`, `"threshold_bytes":16384`, `"excerpt_runes_each_side":2048`, `"eligible_bash_results":0`, `"compacted_bash_results":0`, `"context_compaction_metrics_available":true`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("benchmark metadata missing %s: %s", expected, output.String())
		}
	}
}
