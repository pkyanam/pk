package experiment

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"github.com/unreallabsai/unreal-agent/harness/tool/bash"
)

func TestCompactResultWithCapturesKeepsPathExcerptsAndExitCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full-output.log")
	if err := os.WriteFile(path, []byte("complete output"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("H", 6_000) + strings.Repeat("M", 5_000) + strings.Repeat("T", 6_000)
	got, exists := compactResultWithCaptures(input, operation.ShellState{OutPath: path, Result: &operation.ShellResult{ExitCode: 7}})
	if !exists {
		t.Fatal("expected live capture file to permit compaction")
	}
	if len(got) >= len(input) || !strings.Contains(got, path) || !strings.Contains(got, "Exit code: 7") || !strings.Contains(got, strings.Repeat("H", 32)) || !strings.Contains(got, strings.Repeat("T", 32)) {
		t.Fatalf("compaction lost path, exit status, or edge excerpts: %q", got)
	}
	if strings.Contains(got, strings.Repeat("H", 1_000)) || strings.Contains(got, strings.Repeat("T", 1_000)) {
		t.Fatal("large original excerpts were not compacted")
	}
}

func TestCompactResultFallsBackWhenCaptureMissing(t *testing.T) {
	input := strings.Repeat("x", ReplayCompactionThreshold+1)
	got, exists := compactResultWithCaptures(input, operation.ShellState{OutPath: t.TempDir() + "/missing", Result: &operation.ShellResult{}})
	if exists || got != input {
		t.Fatal("missing capture must preserve original result")
	}
}

func TestRegistryCompactsLargeBashResultOnceAndReportsMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture")
	if err := os.WriteFile(path, []byte("full shell output"), 0o600); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("large test output line\n", 1_000)
	stateBytes, err := json.Marshal(operation.ShellState{OutPath: path, Result: &operation.ShellResult{Out: full, OutSize: int64(len(full)), ExitCode: 0}})
	if err != nil {
		t.Fatal(err)
	}
	op := operation.Operation{ID: "op-1", Type: operation.TypeShell, Version: operation.VersionShell, Status: operation.StatusCompleted, MaxOutputLength: operation.DefaultMaxOutputLength, State: stateBytes}
	counters := &ReplayCompactionCounters{}
	registry := CompactCapturedShellResults(tool.NewRegistry(tool.StaticTranslators{Bash: bash.New(bash.Config{})}, tool.BashName), counters)
	translator, ok := registry.Resolve(tool.BashName)
	if !ok {
		t.Fatal("Bash translator not registered")
	}
	result, err := translator.TranslateResult("call-1", tool.CallStatus{}, []operation.Operation{op})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 1 || len(result.Output[0].Value) >= len(full) || !strings.Contains(result.Output[0].Value, path) {
		t.Fatalf("unexpected compacted result: %#v", result)
	}
	got := counters.Snapshot()
	if got.EligibleResults != 1 || got.CompactedResults != 1 || got.OriginalBytes != int64(len(full)) || got.StoredBytes != int64(len(result.Output[0].Value)) {
		t.Fatalf("unexpected counts: %+v", got)
	}
}

func TestRegistryLeavesSmallBashResultAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture")
	if err := os.WriteFile(path, []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateBytes, err := json.Marshal(operation.ShellState{OutPath: path, Result: &operation.ShellResult{Out: "ok\n", OutSize: 3}})
	if err != nil {
		t.Fatal(err)
	}
	op := operation.Operation{ID: "op-1", Type: operation.TypeShell, Version: operation.VersionShell, Status: operation.StatusCompleted, MaxOutputLength: operation.DefaultMaxOutputLength, State: stateBytes}
	counters := &ReplayCompactionCounters{}
	registry := CompactCapturedShellResults(tool.NewRegistry(tool.StaticTranslators{Bash: bash.New(bash.Config{})}, tool.BashName), counters)
	translator, _ := registry.Resolve(tool.BashName)
	result, err := translator.TranslateResult("call-1", tool.CallStatus{}, []operation.Operation{op})
	if err != nil || len(result.Output) != 1 || result.Output[0].Value != "ok\n" {
		t.Fatalf("small Bash result = %#v, %v", result, err)
	}
	if got := counters.Snapshot(); got.EligibleResults != 0 || got.CompactedResults != 0 {
		t.Fatalf("unexpected counters: %+v", got)
	}
}
