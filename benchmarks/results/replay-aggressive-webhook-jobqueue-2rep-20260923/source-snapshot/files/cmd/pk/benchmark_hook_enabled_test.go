//go:build pkbench

package main

import (
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
