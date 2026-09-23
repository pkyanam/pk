package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReloadHandoffRoundTripAndOneTimeConsume(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	t.Setenv("PK_HOME", home)
	token, err := newReloadToken()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_RELOAD_TOKEN", token)
	t.Setenv("PK_RELOAD_SUPERVISOR_PID", "1234")
	want := reloadHandoff{Workspace: workspace, SessionID: "session-123", Model: "custom/model-id-v2", Effort: "medium", ProviderID: "fixture-provider"}
	if err := writeReloadHandoff(want); err != nil {
		t.Fatal(err)
	}
	path := reloadHandoffPath(filepath.Join(home, "reload"), token)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("handoff mode=%o", info.Mode().Perm())
	}
	got, err := consumeReloadHandoff(token, 1234)
	if err != nil {
		t.Fatal(err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != token || got.SupervisorPID != 1234 || got.Workspace != resolvedWorkspace || got.SessionID != want.SessionID || got.Model != want.Model || got.Effort != want.Effort || got.ProviderID != want.ProviderID {
		t.Fatalf("handoff mismatch: %#v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("handoff was not consumed: %v", err)
	}
	if _, err := consumeReloadHandoff(token, 1234); !os.IsNotExist(err) {
		t.Fatalf("second consume error=%v", err)
	}
}

func TestReloadHandoffRejectsWrongSupervisorAndMalformedFile(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	t.Setenv("PK_HOME", home)
	token, err := newReloadToken()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_RELOAD_TOKEN", token)
	t.Setenv("PK_RELOAD_SUPERVISOR_PID", "77")
	if err := writeReloadHandoff(reloadHandoff{Workspace: workspace, SessionID: "s1", Model: "gpt-6-luna", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeReloadHandoff(token, 78); err == nil || !strings.Contains(err.Error(), "different supervisor") {
		t.Fatalf("wrong supervisor error=%v", err)
	}
	path := reloadHandoffPath(filepath.Join(home, "reload"), token)
	if err := os.WriteFile(path, []byte(`{"token":"`+token+`","owner_pid":77,"workspace":"`+workspace+`","session_id":"x","model":"gpt-6-luna","effort":"low"} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeReloadHandoff(token, 77); err == nil || !strings.Contains(err.Error(), "one JSON object") {
		t.Fatalf("trailing data error=%v", err)
	}
}

func TestReloadHandoffRejectsUnpinnedOrUnsafeState(t *testing.T) {
	workspace := t.TempDir()
	cases := []reloadHandoff{
		{SupervisorPID: 1, Workspace: "relative", SessionID: "s1", Model: "gpt-6-luna", Effort: "low"},
		{SupervisorPID: 1, Workspace: workspace, SessionID: "s1\n--help", Model: "gpt-6-luna", Effort: "low"},
		{SupervisorPID: 1, Workspace: workspace, SessionID: "s1", Model: "gpt-6-luna", Effort: "none"},
		{SupervisorPID: 1, Workspace: filepath.Join(workspace, "missing"), SessionID: "s1", Model: "gpt-6-luna", Effort: "low"},
	}
	for i, h := range cases {
		if err := validateReloadHandoff(&h, 1); err == nil {
			t.Errorf("case %d unexpectedly valid", i)
		}
	}
}

func TestReloadArgumentsAreBoundedStateOnly(t *testing.T) {
	h := reloadHandoff{Workspace: "/tmp/work space", SessionID: "session", Model: "model id", Effort: "high", ProviderID: "fixture-provider"}
	want := []string{"--workspace", h.Workspace, "--session", h.SessionID, "--model", h.Model, "--effort", h.Effort, "--provider", h.ProviderID}
	got := reloadLaunchArgs(h)
	if len(got) != len(want) {
		t.Fatalf("args=%#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args=%#v", got)
		}
	}
	data, err := json.Marshal(h)
	if err != nil || len(data) > maxReloadHandoff {
		t.Fatalf("bounded handoff marshal error=%v len=%d", err, len(data))
	}

	fresh := reloadHandoff{Workspace: h.Workspace, Model: h.Model, Effort: h.Effort, ProviderID: "native"}
	freshArgs := reloadLaunchArgs(fresh)
	for _, arg := range freshArgs {
		if arg == "--session" {
			t.Fatalf("fresh-session reload unexpectedly includes --session: %q", freshArgs)
		}
	}
	if len(freshArgs) != 8 || freshArgs[6] != "--provider" || freshArgs[7] != "native" {
		t.Fatalf("fresh-session reload args=%q", freshArgs)
	}
}
