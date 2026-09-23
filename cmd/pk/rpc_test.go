package main

import (
	"bytes"
	"context"
	"encoding/json"
	jsontext "encoding/json/jsontext"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func TestRPCRejectsUnsupportedVersionAsVersionedJSONL(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := rpcMain(context.Background(), strings.NewReader(`{"version":9,"id":"c1","type":"start"}`+"\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rpcMain exit = %d; stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("diagnostics leaked for protocol error: %q", stderr.String())
	}
	var event rpcEvent
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); err != nil {
		t.Fatalf("decode response: %v; output=%q", err, stdout.String())
	}
	if event.Version != 1 || event.ID != "c1" || event.Type != "error" {
		t.Fatalf("event=%+v", event)
	}
}

func TestRPCReadySurvivesMissingCredentialsSoLoginCanRecover(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_HOME", home)
	workspace := t.TempDir()
	commands := strings.Join([]string{
		`{"version":1,"id":"start-1","type":"start","payload":{"workspace":"` + workspace + `"}}`,
		`{"version":1,"id":"prompt-1","type":"prompt","payload":{"text":"hello"}}`,
		`{"version":1,"id":"shutdown-1","type":"shutdown"}`,
	}, "\n") + "\n"
	var stdout, stderr bytes.Buffer
	if code := rpcMain(context.Background(), strings.NewReader(commands), &stdout, &stderr); code != 0 {
		t.Fatalf("rpcMain exit=%d stderr=%q", code, stderr.String())
	}
	var events []rpcEvent
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var event rpcEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) < 3 || events[0].Type != "ready" {
		t.Fatalf("events=%+v", events)
	}
	var payload map[string]any
	for _, event := range events {
		if event.Type == "error" && event.ID == "prompt-1" {
			payload = event.Payload.(map[string]any)
		}
	}
	if payload == nil || payload["request_type"] != "prompt" {
		t.Fatalf("missing recoverable prompt error: %+v", events)
	}
}

func TestLocateUIEntryHonorsConfiguredAsset(t *testing.T) {
	entry := filepath.Join(t.TempDir(), "main.js")
	if err := os.WriteFile(entry, []byte("// tui"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_UI_ENTRY", entry)
	got, err := locateUIEntry(filepath.Join(t.TempDir(), "pk"))
	if err != nil {
		t.Fatal(err)
	}
	if got != entry {
		t.Fatalf("entry=%q want %q", got, entry)
	}
}

func TestLocateUIEntryUsesInstalledExecutableFromAnotherWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, ".local", "bin")
	uiDir := filepath.Join(root, ".local", "lib", "pk", "ui", "dist")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(uiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(binDir, "pk")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(uiDir, "main.js")
	if err := os.WriteFile(entry, []byte("// tui"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	got, err := locateUIEntry(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got != entry {
		t.Fatalf("entry=%q want %q", got, entry)
	}
}

func TestRecentSessionHistoryReplaysOnlyUserAndAssistantMessages(t *testing.T) {
	ctx := context.Background()
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("session-history")
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	input := inbox.Input{ID: "input-1", Kind: inbox.InputExternal, Payload: jsontext.Value(`"user prompt"`)}
	if err := store.AppendInput(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	turnID := session.TurnID("turn-1")
	if err := store.AppendTurn(ctx, id, session.Turn{ID: turnID, Type: session.TurnRegular}); err != nil {
		t.Fatal(err)
	}
	response := sessionstore.ModelResponse{TurnID: turnID, Response: llm.Response{ID: "response-1", Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "analysis", Phase: "analysis"}}, {Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "answer", Phase: "final"}}}}}
	if err := store.AppendModelResponse(ctx, id, response); err != nil {
		t.Fatal(err)
	}
	entries, truncated, err := recentSessionHistory(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(entries) != 2 {
		t.Fatalf("history=%+v truncated=%v", entries, truncated)
	}
	if entries[0].Role != "user" || entries[0].Text != "user prompt" || entries[1].Role != "assistant" || entries[1].Text != "answer" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestRecentSessionHistoryKeepsLatestRowsBeyondPageWindow(t *testing.T) {
	ctx := context.Background()
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("session-long")
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	const total = 820
	for i := 0; i < total; i++ {
		text := "prompt-" + strconv.Itoa(i)
		payload, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		input := inbox.Input{ID: inbox.ID("input-" + strconv.Itoa(i)), Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}
		if err := store.AppendInput(ctx, id, input); err != nil {
			t.Fatal(err)
		}
	}
	entries, truncated, err := recentSessionHistory(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(entries) != maxHistoryEntries {
		t.Fatalf("rows=%d truncated=%v", len(entries), truncated)
	}
	if entries[0].Text != "prompt-720" || entries[len(entries)-1].Text != "prompt-819" {
		t.Fatalf("history window starts/ends at %q/%q", entries[0].Text, entries[len(entries)-1].Text)
	}
	if strings.Contains(entries[0].Text, "prompt-0") {
		t.Fatalf("old prefix appeared in tail window: %q", entries[0].Text)
	}
}

func TestTailUTF8TruncationKeepsValidText(t *testing.T) {
	got := tailUTF8("before 🌙 after", 8)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "after") {
		t.Fatalf("tail=%q valid=%v", got, utf8.ValidString(got))
	}
}

func TestConfigCommandPersistsSelectedDefaults(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := runConfigCommand([]string{"set", "model", "gpt-6-astra"}, &out, &errOut); code != 0 {
		t.Fatalf("set model exit=%d: %s", code, errOut.String())
	}
	if code := runConfigCommand([]string{"set", "effort", "high"}, &out, &errOut); code != 0 {
		t.Fatalf("set effort exit=%d: %s", code, errOut.String())
	}
	options, _, _, err := parseRunArgs([]string{"-p", "hello", "--workspace", t.TempDir()}, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if options.Model != "gpt-6-astra" || options.Effort != "high" {
		t.Fatalf("parsed model/effort=%s/%s", options.Model, options.Effort)
	}
}

func TestConfigRejectsUnsupportedNoneEffort(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := runConfigCommand([]string{"set", "effort", "none"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "not supported by the current adapter") {
		t.Fatalf("code=%d error=%q", code, errOut.String())
	}
}
