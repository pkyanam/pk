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
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestProjectHistoryCollapsesToolLifecycleAndKeepsStableSequence(t *testing.T) {
	plan := operation.RemoteJobPlan{Type: "test", Version: 1, Data: jsontext.Value(`{}`)}
	spec, err := operation.NewRemoteJobSpec(plan)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(operation.RemoteJobState{Plan: plan, TerminalResult: "created report"})
	if err != nil {
		t.Fatal(err)
	}
	items := []sessionstore.Item{
		{Sequence: 1, Kind: sessionstore.ItemInput, Data: inbox.Input{Kind: inbox.InputExternal, Payload: jsontext.Value(`"inspect files"`)}},
		{Sequence: 2, Kind: sessionstore.ItemModelResponse, Data: sessionstore.ModelResponse{TurnID: "turn-a", Response: llm.Response{Output: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "analysis", Text: "private chain of thought"}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-1", Name: "Bash", Arguments: `{"command":"ls","api_key":"dont-show"}`}},
		}}}},
		{Sequence: 3, Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn-a", CallID: "call-1", Status: tool.CallStatus{WaitingFor: []operation.ID{"op-1"}}, Operations: []operation.Operation{{ID: "op-1", Type: operation.TypeRemoteJob, Version: operation.VersionRemoteJob, Status: operation.StatusReady}}}},
		{Sequence: 4, Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn-a", CallID: "call-1", Status: tool.CallStatus{WaitingFor: []operation.ID{"op-1"}}, Operations: []operation.Operation{{ID: "op-1", Type: operation.TypeRemoteJob, Version: operation.VersionRemoteJob, Status: operation.StatusCompleted, MaxOutputLength: spec.MaxOutputLength, State: jsontext.Value(state)}}}},
		{Sequence: 5, Kind: sessionstore.ItemModelResponse, Data: sessionstore.ModelResponse{TurnID: "turn-a", Response: llm.Response{Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "Done."}}}}}},
	}
	entries, earlier, truncated := projectHistory(items)
	if earlier || truncated || len(entries) != 3 {
		t.Fatalf("projection entries=%+v earlier=%v truncated=%v", entries, earlier, truncated)
	}
	toolRow := entries[1]
	if toolRow.Role != "tool" || toolRow.Sequence != 3 || toolRow.ToolCallID != "call-1" || toolRow.Name != "Bash" || toolRow.State != "completed" {
		t.Fatalf("tool history row=%+v", toolRow)
	}
	if !strings.Contains(toolRow.Text, `"command":"ls"`) || !strings.Contains(toolRow.Text, "created report") || !strings.Contains(toolRow.Text, "[redacted]") {
		t.Fatalf("tool detail=%q", toolRow.Text)
	}
	if strings.Contains(toolRow.Text, "dont-show") || strings.Contains(entries[2].Text, "private chain of thought") {
		t.Fatalf("sensitive history leaked: %+v", entries)
	}
	redacted := redactHistoryText("Bearer abc.one Authorization: Bearer def.two")
	if strings.Contains(redacted, "abc.one") || strings.Contains(redacted, "def.two") || strings.Count(redacted, "[redacted]") != 2 {
		t.Fatalf("multiple bearer values were not redacted: %q", redacted)
	}
	if got := redactHistoryJSON(`{"apiKey":"no-show","public":"ok"}`); strings.Contains(got, "no-show") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("camelCase key was not redacted: %q", got)
	}
}

func TestHistoryToolStateRequiresAllOperationsTerminal(t *testing.T) {
	waiting := []operation.ID{"done", "pending"}
	completed := operation.Operation{ID: "done", Status: operation.StatusCompleted}
	ready := operation.Operation{ID: "pending", Status: operation.StatusReady}
	for _, operations := range [][]operation.Operation{{completed, ready}, {ready, completed}} {
		state, _ := historyToolState(tool.CallStatus{WaitingFor: waiting}, operations)
		if state != "running" {
			t.Fatalf("mixed operation states reported %q: %+v", state, operations)
		}
	}
	state, _ := historyToolState(tool.CallStatus{WaitingFor: waiting}, []operation.Operation{completed, {ID: "pending", Status: operation.StatusCompleted}})
	if state != "completed" {
		t.Fatalf("all terminal operations reported %q", state)
	}
}

func TestProjectHistoryBoundsToolEntriesAndPreservesLegacyProjection(t *testing.T) {
	ordinary, _, _ := projectHistory([]sessionstore.Item{{Sequence: 1, Kind: sessionstore.ItemInput, Data: inbox.Input{Kind: inbox.InputExternal, Payload: jsontext.Value(`"ordinary text mentioning User-provided file attachments"`)}}})
	if len(ordinary) != 1 || !strings.Contains(ordinary[0].Text, "User-provided file attachments") {
		t.Fatalf("ordinary user text was modified: %+v", ordinary)
	}
	items := []sessionstore.Item{}
	for i := 0; i < maxHistoryEntries+5; i++ {
		turnID := session.TurnID("turn-" + strconv.Itoa(i))
		callID := "call-" + strconv.Itoa(i)
		items = append(items,
			sessionstore.Item{Sequence: sessionstore.Sequence(2 + i*2), Kind: sessionstore.ItemModelResponse, Data: sessionstore.ModelResponse{TurnID: turnID, Response: llm.Response{Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: callID, Name: "Read", Arguments: `{}`}}}}}},
			sessionstore.Item{Sequence: sessionstore.Sequence(3 + i*2), Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: turnID, CallID: callID, Status: tool.CallStatus{}}},
		)
	}
	entries, earlier, truncated := projectHistory(items)
	if len(entries) != maxHistoryEntries || !earlier || !truncated {
		t.Fatalf("bounded history len=%d earlier=%v truncated=%v", len(entries), earlier, truncated)
	}
	if entries[len(entries)-1].Role != "tool" {
		t.Fatalf("last history entry=%+v", entries[len(entries)-1])
	}
}

func TestProjectHistoryToolRowsSurviveCursorBoundaryAndParallelCalls(t *testing.T) {
	response := sessionstore.ModelResponse{TurnID: "turn", Response: llm.Response{Output: []llm.Item{
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "a", Name: "Read", Arguments: `{}`}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "b", Name: "Bash", Arguments: `{}`}},
	}}}
	items := []sessionstore.Item{
		{Sequence: 1, Kind: sessionstore.ItemModelResponse, Data: response},
		{Sequence: 2, Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn", CallID: "a", Status: tool.CallStatus{WaitingFor: []operation.ID{"pending-a"}}}},
		{Sequence: 3, Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn", CallID: "b", Status: tool.CallStatus{WaitingFor: []operation.ID{"pending-b"}}}},
		{Sequence: 4, Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn", CallID: "a", Status: tool.CallStatus{Error: "failed"}}},
		{Sequence: 5, Kind: sessionstore.ItemToolCallStatus, Data: sessionstore.ToolCallStatus{TurnID: "turn", CallID: "b", Status: tool.CallStatus{Error: "failed"}}},
	}
	all, _, _ := projectHistory(items)
	if len(all) != 2 || all[0].Sequence != 2 || all[1].Sequence != 3 || all[0].ToolCallID != "a" || all[1].ToolCallID != "b" || all[0].State != "failed" || all[1].State != "failed" {
		t.Fatalf("parallel tool projection=%+v", all)
	}
	beforeLastStatus, _, _ := projectHistory(items[:4])
	if len(beforeLastStatus) != 2 || beforeLastStatus[0].State != "failed" || beforeLastStatus[1].State != "running" {
		t.Fatalf("history before cursor should show state persisted by that point: %+v", beforeLastStatus)
	}
}

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
	if started := readRPCEvent(t, sink); started.Type != "history_page_started" {
		t.Fatalf("history page start event=%+v", started)
	}
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
	sink := &rpcEventSink{events: make(chan []byte, 2)}
	server := &rpcServer{ctx: ctx, output: sink, diagnostics: io.Discard, sessionDir: sessionDir, session: sessionID, started: true, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "latest", Type: "history_before", Payload: json.RawMessage(`{"session_id":"fresh-session-history","before_sequence":0}`)}, make(chan turnDone, 1))
	if started := readRPCEvent(t, sink); started.Type != "history_page_started" {
		t.Fatalf("latest history start event=%+v", started)
	}
	event := readRPCEvent(t, sink)
	if event.Type != "history_page" || event.ID != "latest" {
		t.Fatalf("latest history event=%+v", event)
	}
	encoded, _ := json.Marshal(event.Payload)
	if !strings.Contains(string(encoded), "first prompt after starting a new session") || !strings.Contains(string(encoded), `"has_earlier":false`) {
		t.Fatalf("latest history payload=%s", encoded)
	}
}

func TestHistoryPageCancellationDoesNotBlockRPCLoop(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, requestTypes: map[string]string{}}
	started := make(chan struct{})
	server.startSkillOperation("page", "history page", "history_page_started", "history_page", map[string]any{}, func(ctx context.Context) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if event := readRPCEvent(t, sink); event.Type != "history_page_started" {
		t.Fatalf("start event=%+v", event)
	}
	<-started
	server.handle(rpcMessage{Version: 1, ID: "cancel", Type: "history_cancel"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "history_cancel_requested" {
		t.Fatalf("cancel event=%+v", event)
	}
	if event := readRPCEvent(t, sink); event.Type != "error" || event.Payload.(map[string]any)["cancelled"] != true {
		t.Fatalf("canceled page terminal event=%+v", event)
	}
}
