package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
)

// TestJournalStatusEmitsSummaryAndCoverage verifies the idle-guarded journal
// RPC answers with the folded summary and the coverage disclosure.
func TestJournalStatusEmitsSummaryAndCoverage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), started: true, session: "0123456789abcdef0123456789abcdef", opts: runner.Options{Workspace: t.TempDir(), WorkspaceJournalRoot: filepath.Join(home, "journal")}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "j1", Type: "journal", Payload: json.RawMessage(`{}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "journal" || event.ID != "j1" {
		t.Fatalf("journal event=%+v", event)
	}
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type %T", event.Payload)
	}
	if payload["available"] != true {
		t.Fatalf("journal should report available: %#v", payload)
	}
	if !strings.Contains(payload["coverage"].(string), "unobserved") {
		t.Fatalf("coverage disclosure missing: %#v", payload)
	}
}

// TestJournalStatusWithoutRootReportsUnavailable keeps sessions without a
// journal honest instead of erroring.
func TestJournalStatusWithoutRootReportsUnavailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), started: true, session: "0123456789abcdef0123456789abcdef", opts: runner.Options{Workspace: t.TempDir()}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "j2", Type: "journal", Payload: json.RawMessage(`{}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "journal" {
		t.Fatalf("journal event=%+v", event)
	}
	payload := event.Payload.(map[string]any)
	if payload["available"] != false {
		t.Fatalf("expected available=false without a journal root: %#v", payload)
	}
}

// TestJournalRestoreRefusesWithoutJournal exercises the restore guard path
// end to end over the RPC surface.
func TestJournalRestoreRefusesWithoutJournal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), started: true, session: "0123456789abcdef0123456789abcdef", opts: runner.Options{Workspace: t.TempDir()}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "r1", Type: "journal_restore", Payload: json.RawMessage(`{"session_id":"0123456789abcdef0123456789abcdef","workspace":"` + t.TempDir() + `"}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "error" || !strings.Contains(event.Payload.(map[string]any)["message"].(string), "no workspace journal") {
		t.Fatalf("expected the no-journal error, got %+v", event)
	}
}

// TestRPCStartConfiguresJournalRoot is the regression test for the TUI path:
// every foreground session must receive the workspace journal root so new
// sessions include WorkspaceDelta. The v0.1.17 release missed this, leaving
// TUI sessions without the tool.
func TestRPCStartConfiguresJournalRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), sessionDir: filepath.Join(home, "sessions"), requestTypes: map[string]string{}, prepareAdapter: func(context.Context, bool, string) (*codexAdapter, error) { return nil, errors.New("not used") }}
	payload := `{"workspace":` + jsonString(t, t.TempDir()) + `,"provider_id":"native","steering":false}`
	server.handle(rpcMessage{Version: 1, ID: "start", Type: "start", Payload: json.RawMessage(payload)}, make(chan turnDone, 1))
	var ready *rpcEvent
	for range 8 {
		event := readRPCEvent(t, sink)
		if event.Type == "ready" {
			ready = &event
			break
		}
	}
	if ready == nil {
		t.Fatal("start never emitted ready")
	}
	if server.opts.WorkspaceJournalRoot == "" {
		t.Fatal("start did not configure the workspace journal root; TUI sessions would lack WorkspaceDelta")
	}
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
