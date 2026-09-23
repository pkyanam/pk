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

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestRPCSteerQueuesAndAcceptsAtModelBoundary(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{
		replies: []adapterReply{
			{response: assistantTextResponse("first", "first answer")},
			{response: assistantTextResponse("second", "second answer")},
		},
		started: make(chan struct{}, 4), continueCh: make(chan struct{}),
	}
	sink := &rpcEventSink{events: make(chan []byte, 128)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions,
		started: true, steeringEnabled: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prompt-1", Type: "prompt", Payload: json.RawMessage(`{"text":"start work"}`)}, finished)
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("initial model request did not start")
	}
	server.handle(rpcMessage{Version: 1, ID: "steer-1", Type: "steer", Payload: json.RawMessage(`{"text":"also check the edge case"}`)}, finished)
	queued := waitSteeringEvent(t, sink, "steer-1", "input_queued")
	if queued.Payload.(map[string]any)["input_id"] != "steer-1" {
		t.Fatalf("queued input ID does not match request: %#v", queued.Payload)
	}
	close(model.continueCh)
	accepted := waitSteeringEvent(t, sink, "steer-1", "input_accepted")
	if accepted.Payload.(map[string]any)["session_id"] == "" {
		t.Fatalf("accepted event has no session: %#v", accepted.Payload)
	}
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("steered follow-up did not reach the model")
	}
	model.mu.Lock()
	if len(model.requests) < 2 {
		model.mu.Unlock()
		t.Fatalf("model requests=%d, want follow-up request", len(model.requests))
	}
	var gotSteering bool
	for _, item := range model.requests[1].Input {
		if message, ok := item.Data.(llm.Message); ok && strings.Contains(message.Text, "also check the edge case") {
			gotSteering = true
		}
	}
	model.mu.Unlock()
	if !gotSteering {
		t.Fatal("accepted steering text was absent from the next model request")
	}
	server.handle(rpcMessage{Version: 1, ID: "cancel-1", Type: "cancel"}, finished)
	select {
	case result := <-finished:
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("steered foreground run did not stop after cancel")
	}
}

func TestRPCSteerCancellationRejectsPendingAndStaleInputs(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{started: make(chan struct{}, 2), continueCh: make(chan struct{})}
	sink := &rpcEventSink{events: make(chan []byte, 128)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions,
		started: true, steeringEnabled: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prompt-2", Type: "prompt", Payload: json.RawMessage(`{"text":"wait"}`)}, finished)
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model request did not start")
	}
	server.handle(rpcMessage{Version: 1, ID: "steer-pending", Type: "steer", Payload: json.RawMessage(`{"text":"do this later"}`)}, finished)
	waitSteeringEvent(t, sink, "steer-pending", "input_queued")
	server.handle(rpcMessage{Version: 1, ID: "cancel-2", Type: "cancel"}, finished)
	select {
	case result := <-finished:
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the active model request")
	}
	rejected := waitSteeringEvent(t, sink, "steer-pending", "input_rejected")
	if rejected.Payload.(map[string]any)["input_id"] != "steer-pending" {
		t.Fatalf("rejection lost request ID: %#v", rejected.Payload)
	}
	server.handle(rpcMessage{Version: 1, ID: "steer-stale", Type: "steer", Payload: json.RawMessage(`{"text":"too late"}`)}, finished)
	waitSteeringEvent(t, sink, "steer-stale", "input_rejected")
}

func TestRPCSteerRejectsAttachmentsWithoutQueueingText(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, steeringEnabled: true, session: "session-x", requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "steer-file", Type: "steer", Payload: json.RawMessage(`{"text":"inspect this","files":["note.txt"]}`)}, make(chan turnDone, 1))
	event := waitSteeringEvent(t, sink, "steer-file", "input_rejected")
	message := event.Payload.(map[string]any)["message"].(string)
	if !strings.Contains(message, "attachments are not supported") {
		t.Fatalf("unexpected rejection message %q", message)
	}
	if server.pendingSteers != nil && len(server.pendingSteers) != 0 {
		t.Fatalf("unsupported attachment was queued: %#v", server.pendingSteers)
	}
}

func TestRPCSteerWaitsForToolBoundary(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	marker := filepath.Join(workspace, "tool-finished")
	args, _ := json.Marshal(map[string]string{"command": "sleep 1; printf done > '" + marker + "'"})
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "tool", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "shell-steer", Name: "Bash", Arguments: string(args)}}}}},
		{response: assistantTextResponse("after-tool", "tool finished")},
	}, started: make(chan struct{}, 3),
	}
	sink := &rpcEventSink{events: make(chan []byte, 128)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions,
		started: true, steeringEnabled: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prompt-tool", Type: "prompt", Payload: json.RawMessage(`{"text":"run tool"}`)}, finished)
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("initial model request did not start")
	}
	waitForRunningToolEvent(t, sink, "prompt-tool")
	server.handle(rpcMessage{Version: 1, ID: "steer-tool", Type: "steer", Payload: json.RawMessage(`{"text":"afterward inspect the result"}`)}, finished)
	waitSteeringEvent(t, sink, "steer-tool", "input_queued")
	accepted := waitSteeringEvent(t, sink, "steer-tool", "input_accepted")
	if accepted.Payload.(map[string]any)["input_id"] != "steer-tool" {
		t.Fatalf("accepted event lost input ID: %#v", accepted.Payload)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("steering accepted before tool completed: %v", err)
	}
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("post-tool model request did not start")
	}
	model.mu.Lock()
	if len(model.requests) < 2 {
		model.mu.Unlock()
		t.Fatalf("model requests=%d, want tool follow-up", len(model.requests))
	}
	var sawSteer bool
	for _, item := range model.requests[1].Input {
		if message, ok := item.Data.(llm.Message); ok && strings.Contains(message.Text, "afterward inspect the result") {
			sawSteer = true
		}
	}
	model.mu.Unlock()
	if !sawSteer {
		t.Fatal("steering was not included at the post-tool model boundary")
	}
	server.handle(rpcMessage{Version: 1, ID: "cancel-tool", Type: "cancel"}, finished)
	select {
	case result := <-finished:
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("steered post-tool run did not stop after cancel")
	}
}

func assistantTextResponse(id, text string) llm.Response {
	return llm.Response{ID: id, Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: text}}}}
}

func waitSteeringEvent(t *testing.T, sink *rpcEventSink, id, typ string) rpcEvent {
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
			if event.ID == id && event.Type == typ {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for RPC event id=%s type=%s", id, typ)
		}
	}
}

func waitForRunningToolEvent(t *testing.T, sink *rpcEventSink, id string) {
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
			if event.ID != id || event.Type != "tool_call" {
				continue
			}
			payload, _ := json.Marshal(event.Payload)
			if strings.Contains(string(payload), `"state":"running"`) || strings.Contains(string(payload), `"status":"running"`) {
				return
			}
		case <-timer.C:
			t.Fatal("timed out waiting for a running tool event")
		}
	}
}
