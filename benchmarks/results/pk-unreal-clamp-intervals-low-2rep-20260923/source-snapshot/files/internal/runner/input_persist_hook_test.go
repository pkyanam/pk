package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type finalAnswerAdapter struct{ calls int }

func (a *finalAnswerAdapter) Respond(_ context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.calls++
	return llm.Response{ID: "answer", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "done"}}}}, nil
}

func TestBeforeInputPersistRunsBeforeDurableInputAppend(t *testing.T) {
	ctx := context.Background()
	sessionDir, workspace := filepath.Join(t.TempDir(), "sessions"), t.TempDir()
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	hookRan := false
	inputSawHook := false
	store.AddObserver(func(_ session.ID, item sessionstore.Item) {
		if item.Kind == sessionstore.ItemInput {
			inputSawHook = hookRan
		}
	})
	adapter := &finalAnswerAdapter{}
	result, err := runner.Run(ctx, runner.Options{Prompt: "test prompt", PromptID: "prompt-1", Workspace: workspace, SessionDir: sessionDir, Store: store, Adapter: adapter, BeforeInputPersist: func(sessionID, inputID string) error {
		if sessionID == "" || inputID != "prompt-1" {
			t.Fatalf("hook IDs session=%q input=%q", sessionID, inputID)
		}
		hookRan = true
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !hookRan || !inputSawHook || adapter.calls == 0 || result.SessionID == "" {
		t.Fatalf("hook=%v input saw hook=%v adapter calls=%d result=%+v", hookRan, inputSawHook, adapter.calls, result)
	}
}

func TestBeforeInputPersistFailurePreventsInputAppend(t *testing.T) {
	ctx := context.Background()
	sessionDir, workspace := filepath.Join(t.TempDir(), "sessions"), t.TempDir()
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &finalAnswerAdapter{}
	wantErr := errors.New("sidecar write failed")
	result, err := runner.Run(ctx, runner.Options{Prompt: "test prompt", PromptID: "prompt-fail", Workspace: workspace, SessionDir: sessionDir, Store: store, Adapter: adapter, BeforeInputPersist: func(string, string) error { return wantErr }})
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Run error=%v", err)
	}
	if adapter.calls != 0 {
		t.Fatalf("model called after persistence hook failure: %d", adapter.calls)
	}
	page, err := store.Items(ctx, session.ID(result.SessionID), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.Kind == sessionstore.ItemInput {
			t.Fatalf("input persisted after hook failure: %+v", item)
		}
	}
}
