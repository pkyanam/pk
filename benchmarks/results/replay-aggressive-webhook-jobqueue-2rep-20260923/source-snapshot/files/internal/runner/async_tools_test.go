package runner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// This integration test uses filesystem gates rather than elapsed-time
// comparisons: both commands must be alive before either may complete.
func TestParallelToolsOverlapEmitProgressAndResumeWithBothResults(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	workspace, sessions := t.TempDir(), t.TempDir()
	firstStarted, secondStarted := filepath.Join(workspace, "first.started"), filepath.Join(workspace, "second.started")
	firstGate, secondGate := filepath.Join(workspace, "first.release"), filepath.Join(workspace, "second.release")
	firstArgs := bashArgs(t, fmt.Sprintf("printf started > %s; while [ ! -f %s ]; do sleep 0.01; done; printf result-first", shellQuote(firstStarted), shellQuote(firstGate)))
	secondArgs := bashArgs(t, fmt.Sprintf("printf started > %s; while [ ! -f %s ]; do sleep 0.01; done; printf result-second", shellQuote(secondStarted), shellQuote(secondGate)))
	adapter := &barrierAdapter{
		first: llm.Response{ID: "launch", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "first", Name: "Bash", Arguments: firstArgs}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "second", Name: "Bash", Arguments: secondArgs}},
		}},
		final:         llm.Response{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "both operations completed"}}}},
		secondRequest: make(chan llm.Request, 1), releaseSecond: make(chan struct{}),
	}
	events := &jsonEventWriter{events: make(chan map[string]any, 1024)}
	runDone := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{Prompt: "run both independent checks", Workspace: workspace, SessionDir: sessions, Adapter: adapter, JSONL: true, Output: events})
		runDone <- err
	}()

	var observed []map[string]any
	for _, marker := range []string{firstStarted, secondStarted} {
		waitForFile(t, ctx, marker)
	}
	for _, callID := range []string{"first", "second"} {
		receiveEvent(t, events.events, &observed, func(event map[string]any) bool {
			return event["type"] == "tool_call" && event["call_id"] == callID && event["state"] == "running"
		})
	}
	if err := os.WriteFile(firstGate, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiveEvent(t, events.events, &observed, func(event map[string]any) bool {
		return event["type"] == "tool_call" && event["call_id"] == "first" && event["state"] == "completed"
	})
	select {
	case <-adapter.secondRequest:
	case <-ctx.Done():
		t.Fatal("model did not request an interim update after the first operation completed")
	}
	adapter.mu.Lock()
	interim := toolResults(adapter.requests[1])
	adapter.mu.Unlock()
	if !strings.Contains(interim["first"], "result-first") || !strings.Contains(interim["second"], "still running") {
		t.Fatalf("interim model turn did not contain first result and second-operation progress: %#v", interim)
	}
	if _, err := os.Stat(secondGate); !os.IsNotExist(err) {
		t.Fatalf("second operation was released before interim model progress: %v", err)
	}
	if err := os.WriteFile(secondGate, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiveEvent(t, events.events, &observed, func(event map[string]any) bool {
		return event["type"] == "tool_call" && event["call_id"] == "second" && event["state"] == "completed"
	})
	close(adapter.releaseSecond)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runner failed after both operations completed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("runner failed to settle after releasing the model barrier")
	}
	for {
		select {
		case event := <-events.events:
			observed = append(observed, event)
		default:
			goto drainedEvents
		}
	}
drainedEvents:

	adapter.mu.Lock()
	requests := append([]llm.Request(nil), adapter.requests...)
	maxActive := adapter.maxActive
	adapter.mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("model requests=%d, want launch, blocked progress, final", len(requests))
	}
	if maxActive != 1 {
		t.Fatalf("concurrent model calls=%d, want serialized model turns", maxActive)
	}
	results := toolResults(requests[2])
	if !strings.Contains(results["first"], "result-first") || !strings.Contains(results["second"], "result-second") {
		t.Fatalf("final model request did not contain both completed results: %#v", results)
	}
	if eventIndex(observed, func(event map[string]any) bool {
		return event["type"] == "assistant" && event["phase"] == "final_answer"
	}) < maxEventIndex(observed, "tool_call", "completed") {
		t.Fatal("final assistant event was emitted before both tool completions")
	}
}

type barrierAdapter struct {
	mu            sync.Mutex
	first, final  llm.Response
	requests      []llm.Request
	active        int
	maxActive     int
	secondRequest chan llm.Request
	releaseSecond chan struct{}
}

func (a *barrierAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	a.active++
	if a.active > a.maxActive {
		a.maxActive = a.active
	}
	a.requests = append(a.requests, request)
	call := len(a.requests)
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.active--; a.mu.Unlock() }()
	switch call {
	case 1:
		return a.first, nil
	case 2:
		select {
		case a.secondRequest <- request:
		default:
		}
		select {
		case <-a.releaseSecond:
			return llm.Response{ID: "progress", Stop: llm.StopComplete}, nil
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	case 3:
		results := toolResults(request)
		if !strings.Contains(results["first"], "result-first") || !strings.Contains(results["second"], "result-second") {
			return llm.Response{}, fmt.Errorf("final request arrived without both operation results: %#v", results)
		}
		return a.final, nil
	default:
		return llm.Response{}, fmt.Errorf("unexpected model request %d", call)
	}
}

func (a *barrierAdapter) Close() error { return nil }

type jsonEventWriter struct{ events chan map[string]any }

func (w *jsonEventWriter) Write(data []byte) (int, error) {
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	w.events <- event
	return len(data), nil
}

func bashArgs(t *testing.T, command string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func waitForFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("command did not reach its barrier %s: %v", filepath.Base(path), ctx.Err())
		case <-ticker.C:
		}
	}
}

func receiveEvent(t *testing.T, ch <-chan map[string]any, seen *[]map[string]any, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for _, event := range *seen {
		if match(event) {
			return event
		}
	}
	for {
		select {
		case event := <-ch:
			*seen = append(*seen, event)
			if match(event) {
				return event
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for runner output event")
		}
	}
}

func toolResults(request llm.Request) map[string]string {
	results := map[string]string{}
	for _, item := range request.Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		result, ok := item.Data.(llm.ToolResult)
		if !ok {
			continue
		}
		var text strings.Builder
		for _, output := range result.Output {
			text.WriteString(output.Value)
		}
		results[result.CallID] = text.String()
	}
	return results
}

func eventIndex(events []map[string]any, match func(map[string]any) bool) int {
	for index, event := range events {
		if match(event) {
			return index
		}
	}
	return -1
}

func maxEventIndex(events []map[string]any, typ, state string) int {
	last := -1
	for index, event := range events {
		if event["type"] == typ && event["state"] == state && index > last {
			last = index
		}
	}
	return last
}
