package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestRPCQueueInputsTurnFinishesWithOpenSteeringStream(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "rpc-final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}}}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard, sessionDir: sessions, started: true, steeringEnabled: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{},
	}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prompt", Type: "prompt", Payload: json.RawMessage(`{"text":"hello"}`)}, finished)
	select {
	case result := <-finished:
		if result.err != nil || strings.TrimSpace(result.text) != "done" {
			t.Fatalf("finished turn=%+v", result)
		}
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("RPC prompt did not finish while its steering input stream remained open")
	}
	server.mu.Lock()
	active := server.active
	server.mu.Unlock()
	if active {
		t.Fatal("RPC still reports an active turn after assistant final")
	}
}

func TestRPCAttachAndNewRetainSteeringCapability(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "seed", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "seed"}}}}}}}
	seed, err := runner.Run(context.Background(), runner.Options{Prompt: "seed", Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium", Adapter: model})
	if err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, sessionDir: sessions, started: true, steeringEnabled: true,
		opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, requestTypes: map[string]string{}}
	assertReadySteering := func(id string) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case raw := <-sink.events:
				var event rpcEvent
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				if event.Type != "ready" {
					continue
				}
				body, _ := json.Marshal(event.Payload)
				var payload struct {
					Capabilities []string `json:"capabilities"`
				}
				_ = json.Unmarshal(body, &payload)
				for _, capability := range payload.Capabilities {
					if capability == "steer" {
						return
					}
				}
				t.Fatalf("%s ready omitted steer capability: %+v", id, payload.Capabilities)
			case <-deadline:
				t.Fatalf("%s did not emit ready", id)
			}
		}
	}
	server.handle(rpcMessage{Version: 1, ID: "attach", Type: "attach", Payload: json.RawMessage(`{"session_id":"` + seed.SessionID + `"}`)}, make(chan turnDone, 1))
	assertReadySteering("attach")
	server.handle(rpcMessage{Version: 1, ID: "new", Type: "new"}, make(chan turnDone, 1))
	assertReadySteering("new")
}
