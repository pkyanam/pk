package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
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

func TestRestoreReportUsesRestoredOperationIDsToFindActualPaths(t *testing.T) {
	items := restoredJournalItems(workspacejournal.RestoreReport{Restored: []string{"op-123"}}, []workspacejournal.Op{{ID: "op-123", Path: "src/main.go"}})
	if !reflect.DeepEqual(uniqueRestoredPaths(items), []string{"src/main.go"}) {
		t.Fatalf("restored paths=%v", uniqueRestoredPaths(items))
	}
	note := restoreReportNote(workspacejournal.RestoreReport{Restored: []string{"op-123"}}, "/work", items, nil)
	if !strings.Contains(note, "Restored to pre-tool state: src/main.go") || strings.Contains(note, "Restored to pre-tool state: op-123") {
		t.Fatalf("restore note confused operation ID for path: %q", note)
	}
	partial := restoreReportNote(workspacejournal.RestoreReport{Restored: []string{"op-123"}}, "/work", items, errors.New("snapshot record failed"))
	if !strings.Contains(partial, "src/main.go") || !strings.Contains(partial, "snapshot record failed") {
		t.Fatalf("partial restore note omitted changed file or error: %q", partial)
	}
}

func TestJournalRestorePersistsPathAccurateModelNoteBeforeCompletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	sessionID := "0123456789abcdef0123456789abcdef"
	workspace := t.TempDir()
	file := filepath.Join(workspace, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	journalRoot := filepath.Join(home, "journal")
	journal, err := workspacejournal.Open(journalRoot, workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Begin(sessionID, "op-1234567890", "call", "EditFile", "src/main.go", []byte("before\n"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := journal.Complete(sessionID, "op-1234567890"); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, "sessions")
	sessions, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Create(context.Background(), session.ID(sessionID)); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, sessionDir: sessionDir, started: true, session: sessionID, opts: runner.Options{Workspace: workspace, WorkspaceJournalRoot: journalRoot}, requestTypes: map[string]string{}}
	server.startJournalRestore("restore-1", sessionID, nil, workspace)
	if event := readRPCEvent(t, sink); event.Type != "journal_restore_started" {
		t.Fatalf("start event=%+v", event)
	}
	event := readRPCEvent(t, sink)
	if event.Type != "journal_restore_completed" {
		t.Fatalf("completion event=%+v", event)
	}
	payload := event.Payload.(map[string]any)
	if got := payload["restored"].([]any); !reflect.DeepEqual(got, []any{"src/main.go"}) {
		t.Fatalf("reported restored paths=%#v", payload["restored"])
	}
	if payload["note_recorded"] != true {
		t.Fatalf("restore note was not recorded: %#v", payload)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "before\n" {
		t.Fatalf("restored file=%q err=%v", data, err)
	}
	assertJournalNoteContainsPath(t, sessions, sessionID, "src/main.go")
}

func TestJournalRestoreCLIReportsActualPathAndPersistsNote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	sessionID := "0123456789abcdef0123456789abcdef"
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	journal, err := workspacejournal.Open(filepath.Join(home, "journal"), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Begin(sessionID, "op-abcdef1234", "call", "EditFile", "note.txt", []byte("before"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := journal.Complete(sessionID, "op-abcdef1234"); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, "sessions")
	sessions, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Create(context.Background(), session.ID(sessionID)); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runJournalRestore(journal, context.Background(), []string{sessionID, "--workspace", workspace, "--yes"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("restore exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "restored: note.txt") || strings.Contains(stdout.String(), "restored: op-abcdef1234") {
		t.Fatalf("CLI report mislabeled operation ID as a path: %q", stdout.String())
	}
	assertJournalNoteContainsPath(t, sessions, sessionID, "note.txt")
}

func TestJournalRestoreRefusesCrossProcessActiveSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	sessionID := "0123456789abcdef0123456789abcdef"
	workspace := t.TempDir()
	file := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(file, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	journalRoot := filepath.Join(home, "journal")
	journal, err := workspacejournal.Open(journalRoot, workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Begin(sessionID, "op-1", "call", "EditFile", "note.txt", []byte("before"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := journal.Complete(sessionID, "op-1"); err != nil {
		t.Fatal(err)
	}
	lease, err := sessionlock.Acquire(filepath.Join(home, "sessions"), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	code := runJournalRestore(journal, context.Background(), []string{sessionID, "--workspace", workspace, "--yes"}, io.Discard, io.Discard)
	if code == 0 {
		t.Fatal("restore should refuse a locked session")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "after" {
		t.Fatalf("active session file changed: %q err=%v", data, err)
	}
}

func assertJournalNoteContainsPath(t *testing.T, store *localfile.Store, sessionID, want string) {
	t.Helper()
	page, err := store.Items(context.Background(), session.ID(sessionID), sessionstore.BeforeFirst, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Kind != sessionstore.ItemInput {
			continue
		}
		input, ok := item.Data.(inbox.Input)
		if !ok || input.Kind != inbox.InputExternal {
			continue
		}
		var note string
		if json.Unmarshal(input.Payload, &note) == nil && strings.Contains(note, want) {
			return
		}
	}
	t.Fatalf("no external journal restore note contains %q", want)
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
