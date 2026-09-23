package runner

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

func TestCapturedOutputCompactionUsesReadableExactCapturesAndKeepsStableSummary(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "stdout.log")
	errPath := filepath.Join(dir, "stderr.log")
	if err := os.WriteFile(outPath, []byte("complete stdout artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errPath, []byte("complete stderr artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout := strings.Repeat("H", 2500) + "middle" + strings.Repeat("T", 2500)
	stderr := "Stderr:\n" + strings.Repeat("E", 5000)
	original := stdout + "\n" + stderr + "\nExit code: 7"
	op := completedShellOperation(t, "shell-1", operation.ShellState{OutPath: outPath, ErrPath: errPath, Result: &operation.ShellResult{Out: stdout, Err: strings.TrimPrefix(stderr, "Stderr:\n"), ExitCode: 7}})
	events := []OutputCompactionEvent{}
	base := tool.NewRegistry(tool.StaticTranslators{Bash: bash.New(bash.Config{})}, tool.BashName)
	store := localOutputCompactionStore{directory: filepath.Join(dir, "summaries")}
	registry := compactCapturedOutputRegistry(base, func(e OutputCompactionEvent) { events = append(events, e) }, store)
	translator, _ := registry.Resolve(tool.BashName)
	first, err := translator.TranslateResult("call-1", tool.CallStatus{}, []operation.Operation{op})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Output) != 1 || len(first.Output[0].Value) >= len(original) {
		t.Fatalf("large result was not compacted: %#v", first)
	}
	for _, want := range []string{outPath, errPath, "Exit code: 7", strings.Repeat("H", 64), strings.Repeat("E", 64)} {
		if !strings.Contains(first.Output[0].Value, want) {
			t.Errorf("summary does not preserve %q", want)
		}
	}
	if strings.Contains(first.Output[0].Value, strings.Repeat("E", 1000)) {
		t.Fatal("full stderr unexpectedly remained in model context")
	}
	if len(events) != 1 || !events[0].Compacted || events[0].OriginalBytes != len(original) || events[0].StoredBytes != len(first.Output[0].Value) || events[0].ExitCode != 7 {
		t.Fatalf("unexpected compaction metric: %+v", events)
	}
	events[0].CapturePaths[0] = "caller mutation"
	if err := os.Remove(outPath); err != nil {
		t.Fatal(err)
	}
	resumedEvents := []OutputCompactionEvent{}
	resumedRegistry := compactCapturedOutputRegistry(base, func(e OutputCompactionEvent) { resumedEvents = append(resumedEvents, e) }, store)
	resumedTranslator, _ := resumedRegistry.Resolve(tool.BashName)
	second, err := resumedTranslator.TranslateResult("call-1", tool.CallStatus{}, []operation.Operation{op})
	if err != nil {
		t.Fatal(err)
	}
	if second.Output[0].Value != first.Output[0].Value {
		t.Fatal("retranslation rewrote the saved summary after capture removal")
	}
	if len(resumedEvents) != 1 || !resumedEvents[0].Replayed {
		t.Fatalf("saved compaction decision was not reported as replayed: %+v", resumedEvents)
	}
}

func TestCapturedOutputCompactionFallsBackWithoutAllRequiredCaptures(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(outPath, []byte("stdout"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout := strings.Repeat("O", 5000)
	stderr := strings.Repeat("E", 5000)
	state := operation.ShellState{OutPath: outPath, ErrPath: filepath.Join(t.TempDir(), "missing-stderr"), Result: &operation.ShellResult{Out: stdout, Err: stderr, ExitCode: 3}}
	op := completedShellOperation(t, "shell-missing", state)
	events := []OutputCompactionEvent{}
	base := tool.NewRegistry(tool.StaticTranslators{Bash: bash.New(bash.Config{})}, tool.BashName)
	store := localOutputCompactionStore{directory: filepath.Join(t.TempDir(), "summaries")}
	translator, _ := compactCapturedOutputRegistry(base, func(e OutputCompactionEvent) { events = append(events, e) }, store).Resolve(tool.BashName)
	result, err := translator.TranslateResult("call-2", tool.CallStatus{}, []operation.Operation{op})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output[0].Value, stdout) || !strings.Contains(result.Output[0].Value, stderr) || !strings.Contains(result.Output[0].Value, "Exit code: 3") {
		t.Fatal("missing capture fallback changed the original stdout/stderr/exit result")
	}
	if len(events) != 1 || events[0].Compacted || !events[0].MissingCapture || events[0].StoredBytes != events[0].OriginalBytes {
		t.Fatalf("fallback metric is not truthful: %+v", events)
	}
}

func TestCapturedOutputCompactionLeavesSmallAndNonterminalResultsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := tool.NewRegistry(tool.StaticTranslators{Bash: bash.New(bash.Config{})}, tool.BashName)
	var events int
	translator, _ := compactCapturedOutputRegistry(base, func(OutputCompactionEvent) { events++ }, nil).Resolve(tool.BashName)
	small := completedShellOperation(t, "small", operation.ShellState{OutPath: path, Result: &operation.ShellResult{Out: "ok"}})
	result, err := translator.TranslateResult("call-3", tool.CallStatus{}, []operation.Operation{small})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output[0].Value != "ok" {
		t.Fatalf("small output changed: %#v", result)
	}
	large := completedShellOperation(t, "pending", operation.ShellState{OutPath: path, Result: &operation.ShellResult{Out: strings.Repeat("x", 5000)}})
	large.Status = operation.StatusAwaiting
	result, err = translator.TranslateResult("call-4", tool.CallStatus{}, []operation.Operation{large})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output[0].Value != "Command is still running." || events != 0 {
		t.Fatalf("nonterminal result was modified or counted: %#v events=%d", result, events)
	}
}

func TestCapturedOutputCompactionFailsClosedOnCorruptResumeSidecar(t *testing.T) {
	state := operation.ShellState{OutPath: filepath.Join(t.TempDir(), "capture"), Result: &operation.ShellResult{Out: strings.Repeat("z", 5000)}}
	if err := os.WriteFile(state.OutPath, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	op := completedShellOperation(t, "corrupt-sidecar", state)
	store := localOutputCompactionStore{directory: filepath.Join(t.TempDir(), "summaries")}
	if err := os.MkdirAll(store.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(op.ID), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := tool.NewRegistry(tool.StaticTranslators{Bash: bash.New(bash.Config{})}, tool.BashName)
	translator, _ := compactCapturedOutputRegistry(base, nil, store).Resolve(tool.BashName)
	if _, err := translator.TranslateResult("call-corrupt", tool.CallStatus{}, []operation.Operation{op}); err == nil {
		t.Fatal("corrupt sidecar was silently replaced with a different prefix")
	}
}

func TestOutputCompactionModeIsFrozenInContextSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		enabled bool
		wantErr bool
	}{
		{name: "legacy remains off", version: 0, enabled: false},
		{name: "legacy cannot enable", version: 0, enabled: true, wantErr: true},
		{name: "enabled resumes enabled", version: outputCompactionSnapshotVersion, enabled: true},
		{name: "enabled cannot turn off", version: outputCompactionSnapshotVersion, enabled: false, wantErr: true},
		{name: "unknown version", version: 99, enabled: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOutputCompactionMode(ContextSnapshot{OutputCompactionVersion: tc.version}, tc.enabled)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validation error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func completedShellOperation(t *testing.T, id string, state operation.ShellState) operation.Operation {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return operation.Operation{ID: operation.ID(id), Type: operation.TypeShell, Version: operation.VersionShell, Status: operation.StatusCompleted, MaxOutputLength: operation.DefaultMaxOutputLength, State: encoded}
}
