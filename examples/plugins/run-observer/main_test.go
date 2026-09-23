package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
)

func TestManifestIsToolFreeAndDeclaresObservedEvents(t *testing.T) {
	manifest, err := extensions.LoadManifest("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tools) != 0 || len(manifest.Commands) != 0 {
		t.Fatalf("observer must not register tools or commands: %+v", manifest)
	}
	want := []string{extensions.LifecycleRunStart, extensions.LifecycleResponseComplete, extensions.LifecycleRunEnd}
	if len(manifest.Hooks) != len(want) {
		t.Fatalf("hooks = %+v, want %v", manifest.Hooks, want)
	}
	for i, hook := range manifest.Hooks {
		if hook.Event != want[i] || hook.Mode != "observe" {
			t.Errorf("hook[%d] = %+v, want event %q in observe mode", i, hook, want[i])
		}
	}
}

func TestInitializeNegotiatesLifecycleWithoutDeclaringTools(t *testing.T) {
	worker := observer{}
	result, err := worker.Initialize(context.Background(), extensions.InitializeParams{
		APIVersion: extensions.ProtocolVersion,
		ID:         "run-observer",
		HostFeatures: []string{
			extensions.HostFeatureLifecycle,
			extensions.HostFeatureToolProgress,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 0 || len(result.Commands) != 0 || len(result.Features) != 1 || result.Features[0] != extensions.HostFeatureLifecycle {
		t.Fatalf("unexpected observer registration: %+v", result)
	}
	result, err = worker.Initialize(context.Background(), extensions.InitializeParams{
		APIVersion: extensions.ProtocolVersion,
		ID:         "run-observer",
	})
	if err != nil || len(result.Features) != 0 {
		t.Fatalf("legacy host negotiation result=%+v err=%v", result, err)
	}
}

func TestObserverLogsOnlyBoundedLifecycleMetadata(t *testing.T) {
	var stderr bytes.Buffer
	worker := observer{stderr: &stderr}
	err := worker.NotifyLifecycle(context.Background(), extensions.LifecycleEvent{
		Type: "response_complete", RunID: "run-1", SessionID: "session-2",
		Model: "gpt-6-luna", Workspace: "/private/workspace", Status: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &got); err != nil {
		t.Fatalf("decode observer line: %v", err)
	}
	for key, want := range map[string]string{
		"event": "response_complete", "run_id": "run-1", "session_id": "session-2",
		"model": "gpt-6-luna", "status": "completed",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	for _, forbidden := range []string{"\"workspace\"", "\"prompt\"", "\"response\"", "\"arguments\"", "\"result\"", "/private/workspace"} {
		if strings.Contains(stderr.String(), forbidden) {
			t.Errorf("observer log contains %q: %s", forbidden, stderr.String())
		}
	}
}
