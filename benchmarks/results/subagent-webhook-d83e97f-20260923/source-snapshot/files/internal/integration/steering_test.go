package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type failFollowupAppendStore struct {
	sessionstore.Store
	mu         sync.Mutex
	inputCount int
	err        error
}

func (store *failFollowupAppendStore) AppendInput(ctx context.Context, id session.ID, input inbox.Input) error {
	store.mu.Lock()
	store.inputCount++
	count := store.inputCount
	store.mu.Unlock()
	if count == 2 {
		return store.err
	}
	return store.Store.AppendInput(ctx, id, input)
}

// boundaryAdapter holds the first model response until the test has queued a
// steering input. It then returns a tool call; the follow-up response checks
// that the input and completed tool result arrive together at the next model
// boundary.
type boundaryAdapter struct {
	mu       sync.Mutex
	started  chan struct{}
	second   chan struct{}
	release  chan struct{}
	toolDone string
	requests []llm.Request
	err      error
}

func (a *boundaryAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	call := len(a.requests)
	a.mu.Unlock()
	if call == 1 {
		close(a.started)
		select {
		case <-a.release:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		args, err := json.Marshal(map[string]string{"command": "touch '" + a.toolDone + "'; printf tool-finished"})
		if err != nil {
			return llm.Response{}, err
		}
		return llm.Response{ID: "tool-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "boundary-tool", Name: "Bash", Arguments: string(args)}}}}, nil
	}
	if call == 2 {
		if _, err := os.Stat(a.toolDone); err != nil {
			a.err = fmt.Errorf("tool not done at next response: %w", err)
		}
		if !requestContainsUserText(request, "please inspect after the current command") {
			a.err = fmt.Errorf("steering missing from request %d", call)
		}
		close(a.second)
		return llm.Response{ID: "final-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "steering applied"}}}}, nil
	}
	return llm.Response{ID: "extra-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "extra response"}}}}, nil
}

func requestContainsUserText(request llm.Request, text string) bool {
	for _, item := range request.Input {
		if item.Type != llm.ItemMessage {
			continue
		}
		message, ok := item.Data.(llm.Message)
		if ok && message.Role == llm.RoleUser && strings.Contains(message.Text, text) {
			return true
		}
	}
	return false
}

func TestQueuedSteeringWaitsForToolBoundaryThenStopsAtAssistantIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	workspace := t.TempDir()
	toolDone := filepath.Join(workspace, "tool-done")
	adapter := &boundaryAdapter{started: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{}), toolDone: toolDone}
	inputs := make(chan runner.Input, 1)
	accepted := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{
			Prompt: "begin", SessionDir: t.TempDir(), Workspace: workspace,
			Adapter: adapter, Inputs: inputs, QueueInputs: true,
		})
		done <- err
	}()
	select {
	case <-adapter.started:
	case <-ctx.Done():
		t.Fatal("first model response did not start")
	}
	inputs <- runner.Input{ID: "steer-1", Text: "please inspect after the current command", Accepted: func(err error) { accepted <- err }}
	close(adapter.release)
	// The next model response after tool settlement must include both the tool
	// result and the queued steering input.
	select {
	case <-adapter.second:
	case <-ctx.Done():
		t.Fatal("model was not called at the safe tool boundary")
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("steering input was not durably accepted: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("steering input was not acknowledged after reaching the boundary")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error after final assistant response = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("queued steering run did not stop after final assistant response")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.err != nil {
		t.Errorf("next model boundary did not include completed tool and steering: %v", adapter.err)
	}
	if len(adapter.requests) != 2 {
		t.Fatalf("model requests = %d, want initial and post-tool boundary", len(adapter.requests))
	}
}

func TestQueueInputsDoesNotKeepNoToolTurnAlive(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	adapter := &textBoundaryAdapter{started: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{})}
	close(adapter.release)
	inputs := make(chan runner.Input) // Deliberately left open, as RPC keeps it open during a turn.
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{Prompt: "begin", SessionDir: t.TempDir(), Workspace: t.TempDir(), Adapter: adapter, Inputs: inputs, QueueInputs: true})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("QueueInputs run waited for its open input channel after assistant final")
	}
}

type textBoundaryAdapter struct {
	mu       sync.Mutex
	started  chan struct{}
	second   chan struct{}
	release  chan struct{}
	requests []llm.Request
}

func (a *textBoundaryAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	call := len(a.requests)
	a.mu.Unlock()
	if call == 1 {
		close(a.started)
		select {
		case <-a.release:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{ID: "commentary", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "working on it"}}}}, nil
	}
	if call == 2 {
		close(a.second)
	}
	return llm.Response{ID: "answer", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "answer"}}}}, nil
}

func TestQueuedSteeringSurvivesTextOnlyResponseAndRunAwaitsInputStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	adapter := &textBoundaryAdapter{started: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{})}
	inputs := make(chan runner.Input, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{Prompt: "begin", SessionDir: t.TempDir(), Workspace: t.TempDir(), Adapter: adapter, Inputs: inputs, QueueInputs: true})
		done <- err
	}()
	select {
	case <-adapter.started:
	case <-ctx.Done():
		t.Fatal("first model response did not start")
	}
	inputs <- runner.Input{ID: "steer-text-only", Text: "follow up after text"}
	close(adapter.release)
	select {
	case <-adapter.second:
	case <-ctx.Done():
		t.Fatal("queued input was dropped after text-only response")
	}
	close(inputs)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("run did not stop when input stream closed")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 2 || !requestContainsUserText(adapter.requests[1], "follow up after text") {
		t.Fatalf("follow-up request missing queued input; requests = %d", len(adapter.requests))
	}
}

func TestCancelWhileSteeringQueuedFailsAcceptanceForDurableReplay(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	adapter := &textBoundaryAdapter{started: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{})}
	inputs := make(chan runner.Input, 1)
	accepted := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{Prompt: "begin", SessionDir: t.TempDir(), Workspace: t.TempDir(), Adapter: adapter, Inputs: inputs, QueueInputs: true})
		done <- err
	}()
	select {
	case <-adapter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first model response did not start")
	}
	inputs <- runner.Input{ID: "steer-cancel", Text: "keep this for resume", Accepted: func(err error) { accepted <- err }}
	cancel()
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("queued input was accepted despite cancellation before durable append")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued input callback was not failed on cancellation")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run() succeeded after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not settle after cancellation")
	}
}

func TestQueuedSteeringPersistenceFailureStopsBeforeNextModelAndRejectsRemainder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	workspace := t.TempDir()
	baseStore, err := localfile.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("injected session-store failure")
	store := &failFollowupAppendStore{Store: baseStore, err: persistErr}
	adapter := &boundaryAdapter{started: make(chan struct{}), second: make(chan struct{}), release: make(chan struct{}), toolDone: filepath.Join(workspace, "tool-done")}
	inputs := make(chan runner.Input, 2)
	acks := []chan error{make(chan error, 1), make(chan error, 1)}
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{
			Prompt: "begin", SessionDir: t.TempDir(), Workspace: workspace,
			Adapter: adapter, Inputs: inputs, QueueInputs: true, Store: store,
		})
		done <- err
	}()
	select {
	case <-adapter.started:
	case <-ctx.Done():
		t.Fatal("first model response did not start")
	}
	for index := range acks {
		ack := acks[index]
		inputs <- runner.Input{ID: fmt.Sprintf("failed-steer-%d", index+1), Text: "queued", Accepted: func(err error) { ack <- err }}
	}
	close(adapter.release)
	for index, ack := range acks {
		select {
		case err := <-ack:
			if err == nil {
				t.Errorf("queued input %d was accepted despite failed persistence", index+1)
			}
		case <-ctx.Done():
			t.Fatalf("queued input %d did not receive persistence error", index+1)
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run() succeeded after steering persistence failed")
		}
	case <-ctx.Done():
		t.Fatal("Run() did not stop after steering persistence failure")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 1 {
		t.Errorf("model requests = %d, want only the blocked initial response", len(adapter.requests))
	}
}
