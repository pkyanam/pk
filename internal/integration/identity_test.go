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
	const toolAvailabilityClause = " When asked about your tools, report only the tools available in this session; workspace documentation may describe tools that are not loaded."
	clauseFound := strings.Contains(newSuffix, toolAvailabilityClause)
	newSuffix = strings.Replace(newSuffix, toolAvailabilityClause, "", 1)
	const integrationRelevanceClause = " Use only tools and integrations relevant to the request; availability alone is not a reason to invoke them."
	integrationClauseFound := strings.Contains(newSuffix, integrationRelevanceClause)
	newSuffix = strings.Replace(newSuffix, integrationRelevanceClause, "", 1)
	const nextActionClause = " When you announce a next action, perform it before ending the turn; finish with results only when the work is complete or a necessary user decision blocks it."
	nextActionClauseFound := strings.Contains(newSuffix, nextActionClause)
	newSuffix = strings.Replace(newSuffix, nextActionClause, "", 1)
	// New identity-aware sessions correct scheduling claims; legacy snapshots
	// retain their original preamble. Normalize only those documented changes.
	for _, pair := range [][2]string{
		{"When calls are running, ending a turn without new calls lets the harness wait for their results. The harness does not schedule periodic heartbeat turns.", "You never have to babysit a running call: harness does it for you. As a backup, if calls are active and nothing has happened for ten minutes, a heartbeat wakes you, and this is an opportunity to check that all is well."},
		{"When no calls are running and your work is complete, finish your reply. pk returns control and saves the session so it can be resumed later.", "Ending a turn with no tool calls while calls are running means you sleep until one finishes; ending a turn with nothing running ends the session, so do that only when the task is complete."},
	} {
		if !strings.Contains(newSuffix, pair[0]) {
			t.Fatalf("missing corrected scheduling clause %q", pair[0])
		}
		newSuffix = strings.Replace(newSuffix, pair[0], pair[1], 1)
	}
	if !newFound || !legacyFound || !clauseFound || !integrationClauseFound || !nextActionClauseFound || newSuffix != legacySuffix {
		t.Fatal("replacing the product identity changed the remainder of the inherited prompt")
	}
}
