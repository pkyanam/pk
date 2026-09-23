package runner

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type contextWarningTestAdapter struct{ calls int }

func (adapter *contextWarningTestAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	adapter.calls++
	return llm.Response{ID: "context-warning-answer", Stop: llm.StopComplete, Output: []llm.Item{{
		Type: llm.ItemMessage,
		Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "done"},
	}}}, nil
}

func TestPrecreatedEmptySessionDoesNotWarnOrRefuseCompactPolicy(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	store, err := localfile.New(sessions)
	if err != nil {
		t.Fatal(err)
	}
	const id = "acp-precreated-session"
	if _, err := store.Create(context.Background(), session.ID(id)); err != nil {
		t.Fatal(err)
	}
	adapter := &contextWarningTestAdapter{}
	var diagnostics strings.Builder
	result, err := Run(t.Context(), Options{
		Prompt: "first prompt", SessionID: id, Workspace: workspace, SessionDir: sessions,
		CompactCapturedOutput: true, Adapter: adapter, Diagnostics: &diagnostics,
	})
	if err != nil || result.SessionID != id || adapter.calls != 1 {
		t.Fatalf("first ACP prompt result=%+v calls=%d err=%v", result, adapter.calls, err)
	}
	if strings.Contains(diagnostics.String(), "no saved prompt context") {
		t.Fatalf("empty pre-created session was reported as historical resume: %s", diagnostics.String())
	}
	snapshot, err := defaultContextSnapshotStore(sessions, workspace).LoadContext(t.Context(), session.ID(id))
	if err != nil || snapshot.OutputCompactionVersion != outputCompactionSnapshotVersion {
		t.Fatalf("fresh context snapshot=%+v err=%v", snapshot, err)
	}
}

func TestHistoricalSessionWithoutSnapshotStillWarnsAndRefusesCompactPolicy(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	store, err := localfile.New(sessions)
	if err != nil {
		t.Fatal(err)
	}
	const id = "historical-session-no-context"
	if _, err := store.Create(context.Background(), session.ID(id)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendInput(context.Background(), session.ID(id), inbox.Input{ID: inbox.ID("old-input"), Kind: inbox.InputExternal, Payload: jsontext.Value(`"old prompt"`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(context.Background(), session.ID(id), session.Turn{ID: "old-turn"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendModelResponse(context.Background(), session.ID(id), sessionstore.ModelResponse{TurnID: "old-turn", Response: llm.Response{ID: "old-response"}}); err != nil {
		t.Fatal(err)
	}
	adapter := &contextWarningTestAdapter{}
	var diagnostics strings.Builder
	_, err = Run(t.Context(), Options{
		Prompt: "continue", SessionID: id, Workspace: workspace, SessionDir: sessions,
		CompactCapturedOutput: true, Adapter: adapter, Diagnostics: &diagnostics,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be enabled while resuming a session without a saved context snapshot") {
		t.Fatalf("historical session compact error=%v", err)
	}
	if !strings.Contains(diagnostics.String(), "no saved prompt context; resuming") {
		t.Fatalf("historical session warning missing: %s", diagnostics.String())
	}
	if adapter.calls != 0 {
		t.Fatalf("historical session reached model adapter %d times", adapter.calls)
	}
}
