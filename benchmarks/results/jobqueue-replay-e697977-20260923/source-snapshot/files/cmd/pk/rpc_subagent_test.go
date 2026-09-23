package main

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/runner"
)

func TestRPCSubagentToolRunsAndStreamsLifecycleEvents(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace, sessions := t.TempDir(), t.TempDir()
	parentAdapter := &subagentScriptAdapter{}
	childAdapter := &subagentScriptAdapter{child: true}
	sink := &rpcEventSink{events: make(chan []byte, 128)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard, sessionDir: sessions, started: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: parentAdapter, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{},
		prepareAdapter: func(context.Context, bool, string) (*codexAdapter, error) {
			return &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: childAdapter, useCodex: true, semaphore: make(chan struct{}, 1)}, nil
		},
	}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "parent-prompt", Type: "prompt", Payload: json.RawMessage(`{"text":"delegate the small task"}`)}, finished)
	select {
	case result := <-finished:
		if result.err != nil || result.text != "parent received child report\n" {
			t.Fatalf("parent result=%+v", result)
		}
		server.completeTurn(result)
	case <-time.After(10 * time.Second):
		t.Fatal("RPC subagent flow timed out")
	}
	var events []subagentEventType
	for len(sink.events) > 0 {
		var event rpcEvent
		if err := json.Unmarshal(<-sink.events, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "subagent" {
			var payload subagentEventType
			data, _ := json.Marshal(event.Payload)
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatal(err)
			}
			events = append(events, payload)
		}
	}
	if len(events) < 2 || parentAdapter.calls < 3 || !childAdapter.closed {
		t.Fatalf("subagent events=%d parent calls=%d child closed=%v", len(events), parentAdapter.calls, childAdapter.closed)
	}
	for _, event := range events {
		if event.ChildID == "" || event.Type == "" {
			t.Fatalf("incomplete subagent event: %+v", event)
		}
	}
}

type subagentEventType struct {
	Type      string `json:"type"`
	ChildID   string `json:"child_id"`
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Text      string `json:"text"`
}
