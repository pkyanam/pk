package goals_test

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/pkyanam/pk/internal/goals"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type goalAdapter struct {
	mu          sync.Mutex
	responses   []llm.Response
	beforeFirst func() error
	calls       int
}

type captureGoalOperation struct{ spec operation.Spec }

func (c *captureGoalOperation) Submit(spec operation.Spec) operation.ID {
	c.spec = spec
	return "goal-operation"
}

func (a *goalAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	if len(a.responses) == 0 {
		a.mu.Unlock()
		return llm.Response{}, io.ErrUnexpectedEOF
	}
	r := a.responses[0]
	a.responses = a.responses[1:]
	a.calls++
	before := a.beforeFirst
	a.beforeFirst = nil
	a.mu.Unlock()
	if before != nil {
		if err := before(); err != nil {
			return llm.Response{}, err
		}
	}
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
			goal, err := store.Set(t.Context(), sessionID, "Finish the active implementation safely")
			if err != nil {
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
			_, err = runner.Run(t.Context(), runner.Options{
				Prompt: "record goal state", PreallocatedNewID: sessionID, SessionDir: t.TempDir(), Workspace: t.TempDir(), Adapter: adapter,
				DecorateRegistry: goals.Decorator(store, sessionID, goal.ID, "turn-1"), Output: io.Discard,
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

func TestStaleGoalToolsCannotMutateReplacementGoal(t *testing.T) {
	for _, name := range []string{"GoalComplete", "GoalBlocker"} {
		t.Run(name, func(t *testing.T) {
			sessionID := "1123456789abcdef0123456789abcdef"
			store := goals.NewStore(t.TempDir())
			old, err := store.Set(t.Context(), sessionID, "Verify the old read-only milestone")
			if err != nil {
				t.Fatal(err)
			}
			args := `{"evidence":"old goal claimed verification passed"}`
			if name == "GoalBlocker" {
				args = `{"blocker":"old provider unavailable"}`
			}
			adapter := &goalAdapter{
				responses: []llm.Response{
					{ID: "stale-tool", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "stale-call", Name: name, Arguments: args}}}},
					{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "handled"}}}},
				},
				beforeFirst: func() error {
					_, err := store.Set(t.Context(), sessionID, "Build the new runnable beta milestone")
					return err
				},
			}
			_, err = runner.Run(t.Context(), runner.Options{
				Prompt: "continue the old goal", PreallocatedNewID: sessionID, SessionDir: t.TempDir(), Workspace: t.TempDir(), Adapter: adapter,
				DecorateRegistry: goals.Decorator(store, sessionID, old.ID, "old-turn"), Output: io.Discard,
			})
			if err != nil {
				t.Fatalf("runner failed on stale %s: %v", name, err)
			}
			current, ok, err := store.Get(t.Context(), sessionID)
			if err != nil || !ok {
				t.Fatalf("replacement missing: ok=%t err=%v", ok, err)
			}
			if current.ID == old.ID || current.Status != goals.Active || current.Turns != 0 || current.BlockerTurns != 0 || current.AwaitingUser {
				t.Fatalf("stale %s changed replacement goal: %#v", name, current)
			}
		})
	}
}

func TestResumedTranslatorRejectsPersistedOldGenerationOperation(t *testing.T) {
	sessionID := "2123456789abcdef0123456789abcdef"
	store := goals.NewStore(t.TempDir())
	old, err := store.Set(t.Context(), sessionID, "Finish the first goal safely")
	if err != nil {
		t.Fatal(err)
	}
	oldRegistry := goals.Decorator(store, sessionID, old.ID, "turn-old")(tool.NewRegistry(tool.StaticTranslators{}))
	oldTranslator, ok := oldRegistry.Resolve("GoalComplete")
	if !ok {
		t.Fatal("old GoalComplete translator missing")
	}
	context := &captureGoalOperation{}
	status := oldTranslator.Translate(context, llm.ToolCall{CallID: "persisted-call", Name: "GoalComplete", Arguments: `{"evidence":"old generation passed its checks"}`})
	if status.Error != "" || len(status.WaitingFor) != 1 {
		t.Fatalf("old operation status=%#v", status)
	}
	newGoal, err := store.Set(t.Context(), sessionID, "Continue with the replacement goal")
	if err != nil {
		t.Fatal(err)
	}
	newRegistry := goals.Decorator(store, sessionID, newGoal.ID, "turn-new")(tool.NewRegistry(tool.StaticTranslators{}))
	newTranslator, ok := newRegistry.Resolve("GoalComplete")
	if !ok {
		t.Fatal("new GoalComplete translator missing")
	}
	_, err = newTranslator.TranslateResult("persisted-call", status, []operation.Operation{{ID: "goal-operation", Type: context.spec.Type, Version: context.spec.Version, Status: operation.StatusCompleted, State: context.spec.State}})
	if err != nil {
		t.Fatalf("resume result: %v", err)
	}
	current, ok, err := store.Get(t.Context(), sessionID)
	if err != nil || !ok || current.ID != newGoal.ID || current.Status != goals.Active {
		t.Fatalf("old persisted operation changed replacement: %#v ok=%t err=%v", current, ok, err)
	}
}
