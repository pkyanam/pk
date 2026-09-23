package main

import (
	"context"
	"encoding/json"
	jsontext "encoding/json/jsontext"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func TestHistoryBeforeReturnsBoundedOlderPagesAndRejectsStaleSession(t *testing.T) {
	ctx := context.Background()
	sessionDir := t.TempDir()
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "history-pagination"
	if _, err := store.Create(ctx, session.ID(sessionID)); err != nil {
		t.Fatal(err)
	}
	const total = 350
	for i := 1; i <= total; i++ {
		text := "prompt-" + strconv.Itoa(i)
		payload, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		input := inbox.Input{ID: inbox.ID("input-" + strconv.Itoa(i)), Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}
		if err := store.AppendInput(ctx, session.ID(sessionID), input); err != nil {
			t.Fatal(err)
		}
	}

	entries, hasEarlier, _, cursor, err := sessionHistoryPage(ctx, store, sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxHistoryEntries || !hasEarlier || entries[0].Text != "prompt-251" || cursor != 251 {
		t.Fatalf("latest page len=%d earlier=%v first=%+v cursor=%d", len(entries), hasEarlier, entries[0], cursor)
	}
	older, hasEarlier, _, nextCursor, err := sessionHistoryPage(ctx, store, sessionID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != maxHistoryEntries || !hasEarlier || older[0].Text != "prompt-151" || older[len(older)-1].Text != "prompt-250" || nextCursor != 151 {
		t.Fatalf("older page len=%d earlier=%v first=%+v last=%+v cursor=%d", len(older), hasEarlier, older[0], older[len(older)-1], nextCursor)
	}

	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: ctx, output: sink, diagnostics: io.Discard, sessionDir: sessionDir, session: sessionID, started: true, opts: runner.Options{Workspace: t.TempDir()}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "page", Type: "history_before", Payload: json.RawMessage(`{"session_id":"` + sessionID + `","before_sequence":` + strconv.FormatUint(cursor, 10) + `}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "history_page" || event.ID != "page" {
		t.Fatalf("history page event=%+v", event)
	}
	server.handle(rpcMessage{Version: 1, ID: "stale", Type: "history_before", Payload: json.RawMessage(`{"session_id":"different","before_sequence":` + strconv.FormatUint(cursor, 10) + `}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" {
		t.Fatalf("stale session cursor event=%+v", event)
	}
}

func TestHistoryPageValidatesCursorBounds(t *testing.T) {
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), session.ID("cursor-bounds")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := sessionHistoryPage(context.Background(), store, "cursor-bounds", ^uint64(0)); err == nil {
		t.Fatal("accepted out-of-range history cursor")
	}
}

func TestFreshSessionCanRequestLatestHistoryWithoutCursor(t *testing.T) {
	ctx := context.Background()
	sessionDir := t.TempDir()
	const sessionID = "fresh-session-history"
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, session.ID(sessionID)); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal("first prompt after starting a new session")
	if err := store.AppendInput(ctx, session.ID(sessionID), inbox.Input{ID: inbox.ID("first-prompt"), Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 1)}
	server := &rpcServer{ctx: ctx, output: sink, diagnostics: io.Discard, sessionDir: sessionDir, session: sessionID, started: true, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "latest", Type: "history_before", Payload: json.RawMessage(`{"session_id":"fresh-session-history","before_sequence":0}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "history_page" || event.ID != "latest" {
		t.Fatalf("latest history event=%+v", event)
	}
	encoded, _ := json.Marshal(event.Payload)
	if !strings.Contains(string(encoded), "first prompt after starting a new session") || !strings.Contains(string(encoded), `"has_earlier":false`) {
		t.Fatalf("latest history payload=%s", encoded)
	}
}
