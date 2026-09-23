package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSelectReplayTasksPreservesDefaultSuite(t *testing.T) {
	got, err := selectReplayTasks("")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, current := range got {
		names = append(names, current.name)
	}
	if want := []string{"routematch", "eventmerge"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("default replay tasks = %#v, want %#v", names, want)
	}
}

func TestJobQueueHoldoutRestoresTestsAfterWorkspaceEdits(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "..", "benchmarks", "tasks", "jobqueue"))
	if err != nil {
		t.Fatal(err)
	}
	workspace, output := t.TempDir(), t.TempDir()
	if err := copyTree(fixture, workspace); err != nil {
		t.Fatal(err)
	}
	// Simulate a model deleting or weakening its visible tests. Holdout must
	// restore the benchmark-owned suite before testing production source.
	if err := os.WriteFile(filepath.Join(workspace, "recovery_test.go"), []byte("package jobqueue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	passed, err := verifyHoldout(context.Background(), fixture, workspace, output, "pk-current", 1, "jobqueue")
	if passed || err == nil || !strings.Contains(err.Error(), "not implemented") || !strings.Contains(err.Error(), "TestRecoverExpiredRequeuesAndCanBeClaimedAgain") {
		t.Fatalf("pristine recovery holdout = passed:%t err:%v", passed, err)
	}
	if _, err := os.Stat(filepath.Join(output, "artifacts", "pk-current-rep1-jobqueue", "recovery.go.txt")); err != nil {
		t.Fatalf("holdout source artifact was not archived: %v", err)
	}
}

func TestSelectReplayTasksSupportsMatchedClampWebhookCohort(t *testing.T) {
	got, err := selectReplayTasks(" clamp, webhook ")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, current := range got {
		names = append(names, current.name)
	}
	if want := []string{"clamp", "webhook"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("selected replay tasks = %#v, want %#v", names, want)
	}
	if got[0].implementationPrompt == "" || got[0].verificationPrompt == "" || got[1].implementationPrompt == "" || got[1].verificationPrompt == "" {
		t.Fatal("selected tasks must each have both new-session and resume prompts")
	}
}

func TestSelectReplayTasksSupportsJobQueueRecoveryFixture(t *testing.T) {
	got, err := selectReplayTasks("jobqueue")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].name != "jobqueue" || !strings.Contains(got[0].verificationPrompt, "Resume") {
		t.Fatalf("selected replay task = %#v", got)
	}
	if timeout := replayWholeTimeout(got); timeout != 20*time.Minute {
		t.Fatalf("jobqueue overall timeout = %s, want 20m", timeout)
	}
}

func TestSelectReplayTasksRejectsInvalidSelection(t *testing.T) {
	for _, selection := range []string{"clamp,", "clamp,clamp", "missing"} {
		if _, err := selectReplayTasks(selection); err == nil {
			t.Errorf("selectReplayTasks(%q) succeeded, want validation error", selection)
		}
	}
}

func TestReplayCompactionProfilesAreExplicitAndReproducible(t *testing.T) {
	threshold, excerpt, engine, mode, err := replayCompactionProfile("standard")
	if err != nil || threshold != 4_096 || excerpt != 768 || engine != "pk-compact-replayed-output" || mode != "compact-replayed-shell-output" {
		t.Fatalf("standard profile = %d/%d %q %q, %v", threshold, excerpt, engine, mode, err)
	}
	threshold, excerpt, engine, mode, err = replayCompactionProfile("aggressive")
	if err != nil || threshold != 1_024 || excerpt != 256 || engine != "pk-compact-replayed-output-aggressive" || mode != "compact-replayed-shell-output-aggressive" {
		t.Fatalf("aggressive profile = %d/%d %q %q, %v", threshold, excerpt, engine, mode, err)
	}
	threshold, excerpt, engine, mode, err = replayCompactionProfile("large-output")
	if err != nil || threshold != 16_384 || excerpt != 2_048 || engine != "pk-compact-replayed-output-large" || mode != "compact-replayed-shell-output-large" {
		t.Fatalf("large-output profile = %d/%d %q %q, %v", threshold, excerpt, engine, mode, err)
	}
	if _, _, _, _, err := replayCompactionProfile("unknown"); err == nil {
		t.Fatal("unknown profile should fail clearly")
	}
}

func TestReplayTaskSelectionRequiresReplayExperimentFlag(t *testing.T) {
	if got := run([]string{"-replay-tasks", "webhook"}); got != 2 {
		t.Fatalf("run with task selection but no replay experiment = %d, want usage error 2", got)
	}
}
