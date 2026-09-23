package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/presentation"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestRPCSteeringAttachmentsStayOrderedAndResumeFromDurableInput(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("attachment marker: cobalt fern"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := &mockModelAdapter{
		replies: []adapterReply{
			{response: assistantTextResponse("initial", "waiting for instructions")},
			{response: assistantTextResponse("steered", "files reviewed")},
			{response: assistantTextResponse("resumed", "continuing from saved context")},
		},
		started: make(chan struct{}, 8), continueCh: make(chan struct{}),
	}
	sink := &rpcEventSink{events: make(chan []byte, 256)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions,
		started: true, steeringEnabled: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prompt-start", Type: "prompt", Payload: json.RawMessage(`{"text":"start"}`)}, finished)
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("initial model request did not start")
	}

	server.handle(rpcMessage{Version: 1, ID: "steer-file", Type: "steer", Payload: json.RawMessage(`{"text":"","files":["notes.txt"]}`)}, finished)
	waitSteeringEvent(t, sink, "steer-file", "input_queued")
	server.handle(rpcMessage{Version: 1, ID: "steer-follow", Type: "steer", Payload: json.RawMessage(`{"text":"after the file, check the edge case"}`)}, finished)
	waitSteeringEvent(t, sink, "steer-follow", "input_queued")
	waitSteeringEvent(t, sink, "steer-file", "attachments_loaded")
	close(model.continueCh)
	acceptedFile := waitSteeringEvent(t, sink, "steer-file", "input_accepted")
	waitSteeringEvent(t, sink, "steer-follow", "input_accepted")
	sessionID := acceptedFile.Payload.(map[string]any)["session_id"].(string)
	if sessionID == "" {
		t.Fatal("attachment steering acceptance omitted session ID")
	}
	pageDir := attachments.PromptPDFPageDir(sessions, sessionID, "steer-file")
	if err := os.MkdirAll(pageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(pageDir, "accepted-page.png")
	if err := os.WriteFile(sentinel, []byte("accepted PDF page"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.handle(rpcMessage{Version: 1, ID: "steer-file", Type: "steer", Payload: json.RawMessage(`{"text":"replace it","files":["notes.txt"]}`)}, finished)
	waitSteeringEvent(t, sink, "steer-file", "input_queued")
	duplicate := waitSteeringEvent(t, sink, "steer-file", "input_rejected")
	if !strings.Contains(duplicate.Payload.(map[string]any)["message"].(string), "already has attachment artifacts") {
		t.Fatalf("duplicate ID rejection=%#v", duplicate.Payload)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "accepted PDF page" {
		t.Fatalf("duplicate steer altered accepted page artifact: data=%q err=%v", data, err)
	}

	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("ordered steers did not reach a follow-up model request")
	}
	model.mu.Lock()
	requestTexts := modelRequestText(model.requests[1])
	model.mu.Unlock()
	fileAt, textAt := strings.Index(requestTexts, "attachment marker: cobalt fern"), strings.Index(requestTexts, "after the file, check the edge case")
	if fileAt < 0 || textAt < 0 || fileAt >= textAt {
		t.Fatalf("steering order/content missing from model request: %q", requestTexts)
	}

	server.handle(rpcMessage{Version: 1, ID: "cancel-after-accept", Type: "cancel"}, finished)
	select {
	case result := <-finished:
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the active follow-up")
	}
	record, err := presentation.Load(sessions, sessionID, "steer-file")
	if err != nil || len(record.Attachments) != 1 || record.Attachments[0].Name != "notes.txt" {
		t.Fatalf("persisted attachment history record=%+v err=%v", record, err)
	}

	server.handle(rpcMessage{Version: 1, ID: "prompt-resume", Type: "prompt", Payload: json.RawMessage(`{"text":"continue from the saved file"}`)}, finished)
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("resumed model request did not start")
	}
	model.mu.Lock()
	resumedText := modelRequestText(model.requests[len(model.requests)-1])
	model.mu.Unlock()
	if !strings.Contains(resumedText, "attachment marker: cobalt fern") || !strings.Contains(resumedText, "after the file, check the edge case") {
		t.Fatalf("durable attachment steers missing after resume: %q", resumedText)
	}
	select {
	case result := <-finished:
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("resumed prompt did not finish")
	}
}

func TestRPCSteeringAttachmentCancellationCleansUnacceptedSidecars(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("do not persist before boundary"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := &mockModelAdapter{started: make(chan struct{}, 2), continueCh: make(chan struct{})}
	sink := &rpcEventSink{events: make(chan []byte, 128)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions,
		started: true, steeringEnabled: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prompt-cancel-files", Type: "prompt", Payload: json.RawMessage(`{"text":"wait"}`)}, finished)
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model request did not start")
	}
	server.handle(rpcMessage{Version: 1, ID: "steer-cancel-files", Type: "steer", Payload: json.RawMessage(`{"text":"read this","files":["notes.txt"]}`)}, finished)
	waitSteeringEvent(t, sink, "steer-cancel-files", "input_queued")
	waitSteeringEvent(t, sink, "steer-cancel-files", "attachments_loaded")
	server.handle(rpcMessage{Version: 1, ID: "cancel-files", Type: "cancel"}, finished)
	select {
	case result := <-finished:
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not interrupt the foreground run")
	}
	waitSteeringEvent(t, sink, "steer-cancel-files", "input_rejected")
	for _, path := range []string{attachments.SessionPDFPageDir(sessions, server.session), presentation.SessionDir(sessions, server.session)} {
		entries, err := os.ReadDir(path)
		if err == nil && len(entries) != 0 {
			t.Fatalf("unaccepted steering left artifacts under %s: %v", path, entries)
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("inspect artifacts under %s: %v", path, err)
		}
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 1 {
		t.Fatalf("model calls=%d; canceled unaccepted input should not dispatch another model request", calls)
	}
}

func modelRequestText(request llm.Request) string {
	var out strings.Builder
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok {
			out.WriteString(message.Text)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func TestRPCSteerRejectsTooManyAttachmentsBeforeQueueing(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, steeringEnabled: true, session: "session-x", requestTypes: map[string]string{}, active: true, activeInputs: make(chan runner.Input, 1), activeSteerRequests: make(chan steeringRequest, 1)}
	payload, _ := json.Marshal(map[string]any{"files": []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, "text": ""})
	server.handle(rpcMessage{Version: 1, ID: "steer-too-many", Type: "steer", Payload: payload}, make(chan turnDone, 1))
	event := waitSteeringEvent(t, sink, "steer-too-many", "input_rejected")
	if !strings.Contains(event.Payload.(map[string]any)["message"].(string), "limit 8") {
		t.Fatalf("unexpected attachment limit response: %#v", event.Payload)
	}
	if len(server.activeSteerRequests) != 0 || len(server.pendingSteers) != 0 {
		t.Fatalf("over-limit request entered steering queue: queued=%d pending=%v", len(server.activeSteerRequests), server.pendingSteers)
	}
}
