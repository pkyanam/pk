package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func createRPCSession(t *testing.T, sessionDir, id, prompt string) {
	t.Helper()
	ctx := context.Background()
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, session.ID(id)); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendInput(ctx, session.ID(id), inbox.Input{ID: inbox.ID("input-" + id), Kind: inbox.InputExternal, Payload: data}); err != nil {
		t.Fatal(err)
	}
}

func TestRPCAttachSwitchesToSavedWorkspace(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	sessionDir := t.TempDir()
	workspace := t.TempDir()
	const id = "attach-saved-workspace"
	createRPCSession(t, sessionDir, id, "resume this workspace")
	digest := sha256.Sum256([]byte(id))
	contextPath := filepath.Join(sessionDir, hex.EncodeToString(digest[:])+".context.json")
	if err := os.WriteFile(contextPath, []byte("{\"Workspace\":"+strconvQuote(workspace)+"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard, started: true,
		sessionDir: sessionDir, opts: runner.Options{Workspace: t.TempDir(), SessionDir: sessionDir},
		requestTypes: map[string]string{},
	}
	server.handle(rpcMessage{Version: 1, ID: "attach", Type: "attach", Payload: json.RawMessage("{\"session_id\":\"" + id + "\"}")}, make(chan turnDone, 1))
	var ready rpcEvent
	for i := 0; i < 3; i++ {
		event := readRPCEvent(t, sink)
		if event.Type == "ready" {
			ready = event
		}
	}
	if ready.Type != "ready" || ready.ID != "attach" {
		t.Fatalf("attach ready event=%+v", ready)
	}
	encoded, _ := json.Marshal(ready.Payload)
	if !strings.Contains(string(encoded), workspace) {
		t.Fatalf("attach did not switch to saved workspace: %s", encoded)
	}
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestRPCSessionSearchArchiveAndRestore(t *testing.T) {
	ctx := context.Background()
	sessionDir := t.TempDir()
	createRPCSession(t, sessionDir, "saved-session", "Look up signed webhook handling")
	createRPCSession(t, sessionDir, "current-session", "Keep current work")
	sink := &rpcEventSink{events: make(chan []byte, 16)}
	server := &rpcServer{ctx: ctx, output: sink, diagnostics: io.Discard, started: true, sessionDir: sessionDir, session: "current-session", requestTypes: map[string]string{}}

	server.handle(rpcMessage{Version: 1, ID: "list", Type: "sessions_list", Payload: json.RawMessage("{\"query\":\"signed\"}")}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "sessions_list_started" {
		t.Fatalf("list start event=%+v", started)
	}
	listed := readRPCEvent(t, sink)
	if listed.Type != "sessions" || listed.ID != "list" {
		t.Fatalf("list event=%+v", listed)
	}
	encoded, _ := json.Marshal(listed.Payload)
	if !strings.Contains(string(encoded), "saved-session") || !strings.Contains(string(encoded), "Look up signed webhook handling") {
		t.Fatalf("session search payload=%s", encoded)
	}

	server.handle(rpcMessage{Version: 1, ID: "archive", Type: "sessions_archive", Payload: json.RawMessage("{\"session_ids\":[\"saved-session\",\"current-session\"]}")}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "sessions_archive_started" {
		t.Fatalf("archive start event=%+v", started)
	}
	archived := readRPCEvent(t, sink)
	if archived.Type != "sessions_archived" || archived.ID != "archive" {
		t.Fatalf("archive event=%+v", archived)
	}
	encoded, _ = json.Marshal(archived.Payload)
	var archiveResult map[string]any
	if err := json.Unmarshal(encoded, &archiveResult); err != nil {
		t.Fatal(err)
	}
	results, _ := archiveResult["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("archive results=%+v", archiveResult)
	}
	first, _ := results[0].(map[string]any)
	second, _ := results[1].(map[string]any)
	if first["ok"] != true || first["trash_id"] == "" || second["ok"] != false {
		t.Fatalf("archive result rows=%+v", results)
	}

	server.handle(rpcMessage{Version: 1, ID: "trash", Type: "sessions_trash_list"}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "sessions_trash_list_started" {
		t.Fatalf("trash start event=%+v", started)
	}
	trash := readRPCEvent(t, sink)
	if trash.Type != "sessions_trash" || trash.ID != "trash" {
		t.Fatalf("trash event=%+v", trash)
	}
	encoded, _ = json.Marshal(trash.Payload)
	var trashResult map[string]any
	if err := json.Unmarshal(encoded, &trashResult); err != nil {
		t.Fatal(err)
	}
	entries, _ := trashResult["sessions"].([]any)
	if len(entries) != 1 {
		t.Fatalf("trash list=%+v", trashResult)
	}
	entry, _ := entries[0].(map[string]any)
	if entry["session_id"] != "saved-session" {
		t.Fatalf("trash item=%+v", entry)
	}

	request, _ := json.Marshal(map[string]any{"trash_ids": []string{entry["trash_id"].(string)}})
	server.handle(rpcMessage{Version: 1, ID: "restore", Type: "sessions_restore", Payload: request}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "sessions_restore_started" {
		t.Fatalf("restore start event=%+v", started)
	}
	restored := readRPCEvent(t, sink)
	if restored.Type != "sessions_restored" || restored.ID != "restore" {
		t.Fatalf("restore event=%+v", restored)
	}
	matches, err := filepath.Glob(filepath.Join(sessionDir, "saved-session.session.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("restored session log missing: %v err=%v", matches, err)
	}

	server.handle(rpcMessage{Version: 1, ID: "archive-again", Type: "sessions_archive", Payload: json.RawMessage("{\"session_ids\":[\"saved-session\"]}")}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "sessions_archive_started" {
		t.Fatalf("second archive start event=%+v", started)
	}
	archiveAgain := readRPCEvent(t, sink)
	if archiveAgain.Type != "sessions_archived" {
		t.Fatalf("second archive event=%+v", archiveAgain)
	}
	server.handle(rpcMessage{Version: 1, ID: "trash-again", Type: "sessions_trash_list"}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "sessions_trash_list_started" {
		t.Fatalf("second trash start event=%+v", started)
	}
	trashAgain := readRPCEvent(t, sink)
	encoded, _ = json.Marshal(trashAgain.Payload)
	var trashAgainPayload map[string]any
	if err := json.Unmarshal(encoded, &trashAgainPayload); err != nil {
		t.Fatalf("decode second trash list: %v", err)
	}
	trashAgainSessions, _ := trashAgainPayload["sessions"].([]any)
	if len(trashAgainSessions) != 1 {
		t.Fatalf("second trash list=%s err=%v", encoded, err)
	}
	trashAgainEntry, _ := trashAgainSessions[0].(map[string]any)
	purgeRequest, _ := json.Marshal(map[string]any{"trash_ids": []string{trashAgainEntry["trash_id"].(string)}})
	server.handle(rpcMessage{Version: 1, ID: "purge", Type: "sessions_purge", Payload: purgeRequest}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "sessions_purge_started" {
		t.Fatalf("purge start event=%+v", started)
	}
	purged := readRPCEvent(t, sink)
	if purged.Type != "sessions_purged" || purged.ID != "purge" {
		t.Fatalf("purge event=%+v", purged)
	}
	if _, err := localfile.New(sessionDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "saved-session.session.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("purged session still exists, stat err=%v", err)
	}
}
