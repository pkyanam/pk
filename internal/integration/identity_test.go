package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

const oldHarnessIdentity = "You run on Unreal Agent Harness built by Unreal Labs."
const pkIdentityTemplate = "You are pk, a local coding agent running in the pk harness. You run on %s. Identify yourself as pk; distinguish the harness from its model and provider."

func systemPromptFromRequest(t *testing.T, request llm.Request) string {
	t.Helper()
	for _, item := range request.Input {
		if item.Type != llm.ItemMessage {
			continue
		}
		message, ok := item.Data.(llm.Message)
		if ok && message.Role == llm.RoleSystem {
			return message.Text
		}
	}
	t.Fatal("request has no system message")
	return ""
}

func TestNewSessionIdentityReplacesOnlyFirstHarnessLineAndResumesExactly(t *testing.T) {
	ctx := t.Context()
	workspace, sessionDir := t.TempDir(), t.TempDir()
	response := llm.Response{ID: "identity-probe", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}
	first := &cacheProbeAdapter{response: response}
	created, err := runner.Run(ctx, runner.Options{Prompt: "start", Workspace: workspace, SessionDir: sessionDir, Adapter: first})
	if err != nil {
		t.Fatalf("initial Run() error = %v", err)
	}
	second := &cacheProbeAdapter{response: response}
	if _, err := runner.Run(ctx, runner.Options{Prompt: "continue", SessionID: created.SessionID, Workspace: workspace, SessionDir: sessionDir, Adapter: second}); err != nil {
		t.Fatalf("resume Run() error = %v", err)
	}
	if len(first.requests) != 1 || len(second.requests) != 1 {
		t.Fatalf("request counts = %d and %d, want one each", len(first.requests), len(second.requests))
	}
	initialPrompt := systemPromptFromRequest(t, first.requests[0])
	resumedPrompt := systemPromptFromRequest(t, second.requests[0])
	if initialPrompt != resumedPrompt {
		t.Fatal("system prefix changed when resuming the new session")
	}
	if !strings.HasPrefix(initialPrompt, fmt.Sprintf(pkIdentityTemplate, "gpt-6-luna")) {
		t.Fatalf("system prompt identity = %q, want pk first", initialPrompt[:min(len(initialPrompt), 100)])
	}
	if strings.HasPrefix(initialPrompt, oldHarnessIdentity) || strings.Contains(initialPrompt, oldHarnessIdentity) {
		t.Fatal("new session still includes the inherited Unreal Agent identity")
	}
	if !strings.Contains(initialPrompt, "You work in turns.") || !strings.Contains(initialPrompt, "You are pk, a local coding agent working in the user's current project.") {
		t.Fatal("identity replacement removed the inherited operational preamble or pk system instructions")
	}
	third := &cacheProbeAdapter{response: response}
	if _, err := runner.Run(ctx, runner.Options{Prompt: "switch model", SessionID: created.SessionID, Workspace: workspace, SessionDir: sessionDir, Model: "gpt-6-sol", Adapter: third}); err != nil {
		t.Fatalf("model-change resume Run() error = %v", err)
	}
	modelChangedPrompt := systemPromptFromRequest(t, third.requests[0])
	if !strings.HasPrefix(modelChangedPrompt, fmt.Sprintf(pkIdentityTemplate, "gpt-6-sol")) {
		t.Fatalf("model-change identity = %q, want selected model", modelChangedPrompt[:min(len(modelChangedPrompt), 140)])
	}
	if _, found := strings.CutPrefix(initialPrompt, fmt.Sprintf(pkIdentityTemplate, "gpt-6-luna")); !found {
		t.Fatal("initial prompt lacks the selected model identity")
	}
}

type legacyContextStore struct {
	snapshot runner.ContextSnapshot
	set      bool
}

func (store *legacyContextStore) LoadContext(_ context.Context, _ session.ID) (runner.ContextSnapshot, error) {
	if !store.set {
		return runner.ContextSnapshot{}, os.ErrNotExist
	}
	return store.snapshot, nil
}

func (store *legacyContextStore) SaveContext(_ context.Context, _ session.ID, snapshot runner.ContextSnapshot) error {
	// Simulate a pre-identity snapshot created by an older pk release.
	snapshot.IdentityTemplate = ""
	store.snapshot, store.set = snapshot, true
	return nil
}

func TestLegacySnapshotKeepsOriginalHarnessIdentity(t *testing.T) {
	ctx := t.Context()
	workspace, sessionDir := t.TempDir(), t.TempDir()
	store := &legacyContextStore{}
	response := llm.Response{ID: "legacy-identity-probe", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}
	first := &cacheProbeAdapter{response: response}
	created, err := runner.Run(ctx, runner.Options{Prompt: "start", Workspace: workspace, SessionDir: sessionDir, ContextSnapshots: store, Adapter: first})
	if err != nil {
		t.Fatalf("initial Run() error = %v", err)
	}
	second := &cacheProbeAdapter{response: response}
	if _, err := runner.Run(ctx, runner.Options{Prompt: "continue", SessionID: created.SessionID, Workspace: workspace, SessionDir: sessionDir, ContextSnapshots: store, Adapter: second}); err != nil {
		t.Fatalf("resume Run() error = %v", err)
	}
	newPrompt := systemPromptFromRequest(t, first.requests[0])
	legacyPrompt := systemPromptFromRequest(t, second.requests[0])
	if !strings.HasPrefix(legacyPrompt, oldHarnessIdentity) {
		t.Fatalf("legacy resumed identity = %q, want original harness line", legacyPrompt[:min(len(legacyPrompt), 100)])
	}
	newSuffix, newFound := strings.CutPrefix(newPrompt, fmt.Sprintf(pkIdentityTemplate, "gpt-6-luna"))
	legacySuffix, legacyFound := strings.CutPrefix(legacyPrompt, oldHarnessIdentity)
	if !newFound || !legacyFound || newSuffix != legacySuffix {
		t.Fatal("replacing the product identity changed the remainder of the inherited prompt")
	}
}
