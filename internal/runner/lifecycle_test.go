package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type lifecycleFailureAdapter struct{ err error }

func (a lifecycleFailureAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	return llm.Response{}, a.err
}

func TestLifecycleObserverReportsPersistedResponseAndRunBoundary(t *testing.T) {
	workspace, sessions := t.TempDir(), filepath.Join(t.TempDir(), "sessions")
	var events []runner.LifecycleEvent
	result, err := runner.Run(context.Background(), runner.Options{
		Prompt: "private prompt text", Workspace: workspace, SessionDir: sessions,
		Model: "gpt-6-luna", Effort: "medium", Adapter: &finalAnswerAdapter{},
		LifecycleObserver: func(event runner.LifecycleEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d lifecycle events, want 3", len(events))
	}
	wantTypes := []string{runner.LifecycleRunStart, runner.LifecycleResponseComplete, runner.LifecycleRunEnd}
	for i, event := range events {
		if event.Type != wantTypes[i] {
			t.Fatalf("event %d type=%q, want %q", i, event.Type, wantTypes[i])
		}
		if event.RunID == "" || event.RunID != events[0].RunID || event.SessionID != result.SessionID || event.Model != "gpt-6-luna" || event.Workspace != workspace {
			t.Fatalf("event %d metadata mismatch: %+v", i, event)
		}
	}
	if events[0].Status != "running" || events[1].Status != "persisted" || events[2].Status != "completed" {
		t.Fatalf("unexpected lifecycle statuses: %+v", events)
	}
}

func TestLifecycleObserverEndsFailedRunWithoutResponseEvent(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	var events []runner.LifecycleEvent
	_, err := runner.Run(context.Background(), runner.Options{
		Prompt: "do not expose this", Workspace: t.TempDir(), SessionDir: filepath.Join(t.TempDir(), "sessions"),
		Adapter:           lifecycleFailureAdapter{err: wantErr},
		LifecycleObserver: func(event runner.LifecycleEvent) { events = append(events, event) },
	})
	if err == nil {
		t.Fatal("expected model failure")
	}
	if len(events) != 2 || events[0].Type != runner.LifecycleRunStart || events[1].Type != runner.LifecycleRunEnd || events[1].Status != "failed" {
		t.Fatalf("unexpected failed lifecycle sequence: %+v", events)
	}
}
