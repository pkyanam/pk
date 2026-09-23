package main

import (
	"bytes"
	"context"
	"encoding/json"
	jsontext "encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type rpcEventSink struct{ events chan []byte }

func (sink *rpcEventSink) Write(data []byte) (int, error) {
	copy := append([]byte(nil), data...)
	select {
	case sink.events <- copy:
	default:
		return 0, errors.New("test event buffer full")
	}
	return len(data), nil
}

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
	reader, writer := io.Pipe()
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- rpcMain(context.Background(), reader, sink, &stderr) }()
	if _, err := fmt.Fprintf(writer, "%s\n%s\n",
		`{"version":1,"id":"start-1","type":"start","payload":{"workspace":"`+workspace+`"}}`,
		`{"version":1,"id":"prompt-1","type":"prompt","payload":{"text":"hello"}}`); err != nil {
		t.Fatal(err)
	}
	var events []rpcEvent
	var payload map[string]any
	deadline := time.After(5 * time.Second)
	for payload == nil {
		select {
		case data := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
			if event.Type == "error" && event.ID == "prompt-1" {
				payload = event.Payload.(map[string]any)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for missing-credential error: %+v", events)
		}
	}
	if len(events) == 0 || events[0].Type != "ready" {
		t.Fatalf("events=%+v", events)
	}
	if payload == nil || payload["request_type"] != "prompt" {
		t.Fatalf("missing recoverable prompt error: %+v", events)
	}
	if _, err := fmt.Fprintln(writer, `{"version":1,"id":"shutdown-1","type":"shutdown"}`); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("rpcMain exit=%d stderr=%q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC server did not stop")
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

func TestRPCPromptAttachmentsReachModelAsNewInput(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	selectedDir := t.TempDir() // Explicit user-selected input may live outside workspace.
	file := filepath.Join(selectedDir, "reference.txt")
	const marker = "ATTACHMENT_SENTINEL_9721"
	if err := os.WriteFile(file, []byte("reference body: "+marker), 0o600); err != nil {
		t.Fatal(err)
	}
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "done", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Read the file."}}}}}}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	payload, _ := json.Marshal(map[string]any{"text": "Summarize this reference.", "files": []string{file}})
	server.handle(rpcMessage{Version: 1, ID: "attached-turn", Type: "prompt", Payload: payload}, finished)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("runner error: %v", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not finish")
	}
	model.mu.Lock()
	requests := append([]llm.Request(nil), model.requests...)
	model.mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("model received no requests")
	}
	var found bool
	for _, request := range requests {
		for _, item := range request.Input {
			if message, ok := item.Data.(llm.Message); ok && strings.Contains(message.Text, marker) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("selected file content did not reach model input; requests=%+v", requests)
	}
	_ = server.adapter.Close()
}

func TestRPCPromptUsesAdapterPreparedDuringAsyncSetup(t *testing.T) {
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "reply", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "adapter is ready"}}}}}}}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	workspace, sessions := t.TempDir(), t.TempDir()
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, requestTypes: map[string]string{}, prepareAdapter: func(context.Context, bool, string) (*codexAdapter, error) { return adapter, nil }}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prepared-turn", Type: "prompt", Payload: json.RawMessage(`{"text":"hello"}`)}, finished)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("runner error: %v", result.err)
		}
		if strings.TrimSpace(result.text) != "adapter is ready" {
			t.Fatalf("result text=%q", result.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not finish")
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls == 0 {
		t.Fatal("prepared adapter was not used by runner")
	}
	if server.adapter != adapter {
		t.Fatal("prepared adapter was not retained on RPC server")
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = adapter.Close()
}

func TestRPCCompletesTwoSequentialPromptsOnOneSession(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "first", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "first answer"}}}}},
		{response: llm.Response{ID: "second", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "second answer"}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	reader, writer := io.Pipe()
	server := &rpcServer{ctx: context.Background(), input: reader, output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	served := make(chan error, 1)
	go func() { served <- server.serve() }()
	writeCommand := func(id, text string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"text": text})
		command, _ := json.Marshal(rpcMessage{Version: 1, ID: id, Type: "prompt", Payload: payload})
		if _, err := writer.Write(append(command, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	waitTurn := func(id string) {
		t.Helper()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			select {
			case raw := <-sink.events:
				var event rpcEvent
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				if event.ID == id && event.Type == "turn_finished" {
					return
				}
				if event.ID == id && event.Type == "error" {
					t.Fatalf("prompt %s failed: %v", id, event.Payload)
				}
			case <-timer.C:
				t.Fatalf("prompt %s did not finish", id)
			}
		}
	}
	writeCommand("prompt-one", "first prompt")
	waitTurn("prompt-one")
	server.mu.Lock()
	if server.active {
		t.Fatal("server remained active after first turn_finished")
	}
	sessionID := server.session
	server.mu.Unlock()
	if sessionID == "" {
		t.Fatal("first prompt did not create a session")
	}
	writeCommand("prompt-two", "second prompt")
	waitTurn("prompt-two")
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls=%d, want two", calls)
	}
	shutdown, _ := json.Marshal(rpcMessage{Version: 1, ID: "shutdown", Type: "shutdown"})
	_, _ = writer.Write(append(shutdown, '\n'))
	_ = writer.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC server did not stop after two turns")
	}
}

func TestRPCTurnFinishesAfterToolThenAssistantResponse(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "printf rpc-tool-result"})
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "tool-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "shell-1", Name: "Bash", Arguments: string(args)}}}}},
		{response: llm.Response{ID: "final-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "command complete"}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	reader, writer := io.Pipe()
	server := &rpcServer{ctx: context.Background(), input: reader, output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	served := make(chan error, 1)
	go func() { served <- server.serve() }()
	prompt, _ := json.Marshal(rpcMessage{Version: 1, ID: "tool-turn", Type: "prompt", Payload: json.RawMessage(`{"text":"run a command"}`)})
	if _, err := writer.Write(append(prompt, '\n')); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	var assistantSeen bool
	for {
		select {
		case raw := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "assistant" && strings.Contains(fmt.Sprint(event.Payload), "command complete") {
				assistantSeen = true
			}
			if event.ID == "tool-turn" && event.Type == "turn_finished" {
				if !assistantSeen {
					t.Fatal("turn_finished arrived before the final assistant event")
				}
				server.mu.Lock()
				active := server.active
				server.mu.Unlock()
				if active {
					t.Fatal("server remained active after tool turn completion")
				}
				goto finished
			}
		case <-deadline.C:
			t.Fatal("RPC turn did not finish after Bash and assistant response")
		}
	}

finished:
	shutdown, _ := json.Marshal(rpcMessage{Version: 1, ID: "shutdown", Type: "shutdown"})
	_, _ = writer.Write(append(shutdown, '\n'))
	_ = writer.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC server did not stop after tool turn")
	}
}

func TestRPCCancelInterruptsBlockedAttachmentSetup(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	entered := make(chan struct{})
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "unexpected", Stop: llm.StopComplete}}}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	server.loadAttachments = func(ctx context.Context, workspace, prompt string, paths []string) (string, []attachments.Attachment, error) {
		close(entered)
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "blocked-turn", Type: "prompt", Payload: json.RawMessage(`{"text":"read it","files":["slow.pdf"]}`)}, finished)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("attachment setup did not start asynchronously")
	}
	server.handle(rpcMessage{Version: 1, ID: "cancel-setup", Type: "cancel"}, finished)
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("setup result error=%v, want context.Canceled", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not interrupt blocked attachment setup")
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 0 {
		t.Fatalf("model calls=%d, expected cancellation before model run", calls)
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = server.adapter.Close()
}

func TestRPCQuestionUsesBrokerAndContinuesRunner(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	args, err := json.Marshal(map[string]any{"question": "Choose a direction?", "choices": []string{"A", "B"}})
	if err != nil {
		t.Fatal(err)
	}
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "ask", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "question-1", Name: "AskUser", Arguments: string(args)}}}}},
		{response: llm.Response{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Thanks, proceeding with A."}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "turn-1", Type: "prompt", Payload: json.RawMessage(`{"text":"Ask me before choosing."}`)}, finished)
	var seenQuestion, seenAnswer, seenAssistant bool
	var eventTypes []string
	deadline := time.After(10 * time.Second)
	for !seenAssistant {
		select {
		case data := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			eventTypes = append(eventTypes, event.Type)
			switch event.Type {
			case "question":
				question := event.Payload.(map[string]any)
				if question["id"] != "question-1" || question["text"] != "Choose a direction?" {
					t.Fatalf("question=%v", question)
				}
				seenQuestion = true
				server.handle(rpcMessage{Version: 1, ID: "answer-1", Type: "answer_question", Payload: json.RawMessage(`{"id":"question-1","answer":"A"}`)}, finished)
			case "question_answered":
				seenAnswer = true
			case "assistant":
				seenAssistant = true
			}
		case <-deadline:
			select {
			case result := <-finished:
				t.Fatalf("timed out; runner returned text=%q error=%v; events=%v", result.text, result.err, eventTypes)
			default:
			}
			t.Fatalf("timed out waiting for question and final assistant answer; events=%v", eventTypes)
		}
	}
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("runner failed after answer: %v", result.err)
		}
		if !strings.Contains(result.text, "Thanks, proceeding with A.") {
			t.Fatalf("final text=%q", result.text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not finish after answer")
	}
	if !seenQuestion || !seenAnswer || !seenAssistant {
		t.Fatalf("events: question=%v answered=%v assistant=%v", seenQuestion, seenAnswer, seenAssistant)
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = server.adapter.Close()
}

func TestRPCCancelQuestionStopsTurnWithoutContinuing(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	args, _ := json.Marshal(map[string]any{"question": "Confirm destructive change?", "kind": "confirmation"})
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "ask", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "question-cancel", Name: "AskUser", Arguments: string(args)}}}}},
		{response: llm.Response{ID: "should-not-run", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "continued without consent"}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "turn-cancel", Type: "prompt", Payload: json.RawMessage(`{"text":"Do the potentially destructive work."}`)}, finished)
	deadline := time.After(10 * time.Second)
	var questionSeen, canceledEvent bool
	for !canceledEvent {
		select {
		case data := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "question" {
				questionSeen = true
				server.handle(rpcMessage{Version: 1, ID: "cancel-1", Type: "cancel_question", Payload: json.RawMessage(`{"id":"question-cancel"}`)}, finished)
			}
			if event.Type == "question_cancelled" {
				canceledEvent = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for cancellation event")
		}
	}
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("runner error=%v, want cancellation", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner stayed blocked after question cancellation")
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if !questionSeen || calls != 1 {
		t.Fatalf("question=%v model calls=%d, want one call and no continuation", questionSeen, calls)
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = server.adapter.Close()
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
