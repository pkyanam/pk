package main

import (
	"reflect"
	"testing"
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

func TestSelectReplayTasksRejectsInvalidSelection(t *testing.T) {
	for _, selection := range []string{"clamp,", "clamp,clamp", "missing"} {
		if _, err := selectReplayTasks(selection); err == nil {
			t.Errorf("selectReplayTasks(%q) succeeded, want validation error", selection)
		}
	}
}

func TestReplayTaskSelectionRequiresReplayExperimentFlag(t *testing.T) {
	if got := run([]string{"-replay-tasks", "webhook"}); got != 2 {
		t.Fatalf("run with task selection but no replay experiment = %d, want usage error 2", got)
	}
}
