package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// requestDrivenQuestionAdapter makes recovery output depend on the actual
// prompt contents, so the test is stable whether resume recovers an interrupted
// tool before or together with the newly submitted prompt.
type requestDrivenQuestionAdapter struct {
	mu       sync.Mutex
	calls    int
	requests []llm.Request
}

func (adapter *requestDrivenQuestionAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	adapter.mu.Lock()
	adapter.calls++
	call := adapter.calls
	adapter.requests = append(adapter.requests, request)
	adapter.mu.Unlock()
	if requestHasUserText(request, "start the next task") {
		return llm.Response{ID: "second-final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "second request settled"}}}}, nil
	}
	if call == 1 {
		arguments, _ := json.Marshal(map[string]any{"question": "Continue?", "kind": "confirmation"})
		return llm.Response{ID: "ask", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "question-1", Name: "AskUser", Arguments: string(arguments)}}}}, nil
	}
	return llm.Response{ID: "recovery", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "ready for the next task"}}}}, nil
}

func (adapter *requestDrivenQuestionAdapter) Close() error { return nil }

func requestHasUserText(request llm.Request, wanted string) bool {
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleUser && message.Text == wanted {
			return true
		}
	}
	return false
}

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

func TestRPCCancelledQuestionReleasesTurnForSecondPrompt(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &requestDrivenQuestionAdapter{}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard,
		sessionDir: sessions, started: true,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low"},
		adapter:      &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		requestTypes: map[string]string{},
	}

	firstFinished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "first", Type: "prompt", Payload: json.RawMessage(`{"text":"begin a task that asks a question"}`)}, firstFinished)
	question := waitRPCType(t, sink, "question")
	if question.ID != "first" {
		t.Fatalf("question correlated to %q, want first prompt", question.ID)
	}
	server.handle(rpcMessage{Version: 1, ID: "answer-cancel", Type: "cancel_question", Payload: json.RawMessage(`{"id":"question-1"}`)}, firstFinished)
	if event := waitRPCType(t, sink, "question_cancelled"); event.ID != "answer-cancel" {
		t.Fatalf("question cancellation correlated to %q", event.ID)
	}
	select {
	case result := <-firstFinished:
		if result.id != "first" || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled first turn=%+v", result)
		}
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled question did not settle the foreground turn")
	}
	server.mu.Lock()
	active := server.active
	server.mu.Unlock()
	if active {
		t.Fatal("turn remained active after canceled question settled")
	}
	model.mu.Lock()
	callsAfterCancel := model.calls
	model.mu.Unlock()
	if callsAfterCancel != 1 {
		t.Fatalf("model calls after question cancellation=%d, want exactly one", callsAfterCancel)
	}

	secondFinished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "second", Type: "prompt", Payload: json.RawMessage(`{"text":"start the next task"}`)}, secondFinished)
	select {
	case result := <-secondFinished:
		if result.err != nil || strings.TrimSpace(result.text) != "second request settled" {
			t.Fatalf("second turn after cancellation=%+v error=%v", result, result.err)
		}
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept and complete a second prompt after question cancellation")
	}
	model.mu.Lock()
	calls := model.calls
	var sawSecondPrompt bool
	for _, request := range model.requests[1:] {
		for _, item := range request.Input {
			if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleUser && message.Text == "start the next task" {
				sawSecondPrompt = true
			}
		}
	}
	model.mu.Unlock()
	if calls < 2 || calls > 3 {
		t.Fatalf("model calls=%d, want cancellation recovery and the second prompt", calls)
	}
	if !sawSecondPrompt {
		t.Fatal("the model request after cancellation did not contain the second prompt")
	}
}

func waitRPCType(t *testing.T, sink *rpcEventSink, typ string) rpcEvent {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case raw := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == typ {
				return event
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for RPC event %s", typ)
		}
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
