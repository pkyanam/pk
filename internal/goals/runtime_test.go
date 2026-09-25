package goals_test

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/pkyanam/pk/internal/goals"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type goalAdapter struct {
	mu        sync.Mutex
	responses []llm.Response
}

func (a *goalAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.responses) == 0 {
		return llm.Response{}, io.ErrUnexpectedEOF
	}
	r := a.responses[0]
	a.responses = a.responses[1:]
	return r, nil
}

func TestGoalToolsRunThroughCoordinator(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		want       goals.Status
	}{
		{"complete", `{"evidence":"go test ./internal/goals passed"}`, goals.Complete},
		{"blocker", `{"blocker":"provider is unavailable"}`, goals.Active},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := "0123456789abcdef0123456789abcdef"
			store := goals.NewStore(t.TempDir())
			if _, err := store.Set(t.Context(), sessionID, "Finish the active implementation safely"); err != nil {
				t.Fatal(err)
			}
			name := "GoalComplete"
			if tc.name == "blocker" {
				name = "GoalBlocker"
			}
			adapter := &goalAdapter{responses: []llm.Response{
				{ID: "tool", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-goal", Name: name, Arguments: tc.args}}}},
				{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "recorded"}}}},
			}}
			_, err := runner.Run(t.Context(), runner.Options{
				Prompt: "record goal state", PreallocatedNewID: sessionID, SessionDir: t.TempDir(), Workspace: t.TempDir(), Adapter: adapter,
				DecorateRegistry: goals.Decorator(store, sessionID, "turn-1"), Output: io.Discard,
			})
			if err != nil {
				t.Fatalf("runner failed processing %s: %v", name, err)
			}
			got, ok, err := store.Get(t.Context(), sessionID)
			if err != nil || !ok {
				t.Fatalf("goal missing: ok=%v err=%v", ok, err)
			}
			if got.Status != tc.want {
				t.Fatalf("status=%s want %s", got.Status, tc.want)
			}
			if tc.name == "blocker" && (got.BlockerTurns != 1 || got.LastBlockerTurn != "turn-1") {
				t.Fatalf("blocker not recorded: %#v", got)
			}
		})
	}
}
