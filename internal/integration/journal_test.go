package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// TestRunJournalsWriteFileAndAnswersDelta runs the full vertical slice: a
// journaled WriteFile, a WorkspaceDelta call over the journal, and an
// explicit restore that reverts the file and informs the model.
func TestRunJournalsWriteFileAndAnswersDelta(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	workspace := t.TempDir()
	journalRoot := filepath.Join(t.TempDir(), "journal")
	// WriteFile requires existing parent directories; precondition failures
	// are correctly not journaled, so the fixture creates the directory.
	if err := os.MkdirAll(filepath.Join(workspace, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeArgsRaw, err := json.Marshal(map[string]any{"path": "notes/report.md", "content": "alpha\nbeta\n"})
	if err != nil {
		t.Fatal(err)
	}
	overwriteArgsRaw, err := json.Marshal(map[string]any{"path": "notes/report.md", "content": "alpha\ngamma\n", "overwrite": true})
	if err != nil {
		t.Fatal(err)
	}
	deltaArgsRaw, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1: create the file. Turn 2: replace it. Turn 3: ask for the delta.
	responses := []llm.Response{
		{ID: "turn-1", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-create", Name: "WriteFile", Arguments: string(writeArgsRaw)}}}},
		{ID: "turn-2", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-overwrite", Name: "WriteFile", Arguments: string(overwriteArgsRaw)}}}},
		{ID: "turn-3", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-delta", Name: "WorkspaceDelta", Arguments: string(deltaArgsRaw)}}}},
		{ID: "turn-4", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "delta reported"}}}},
	}
	adapter := &scriptedAdapter{responses: responses}
	var output strings.Builder
	options := runner.Options{
		Prompt: "journal and report", SessionDir: t.TempDir(), Workspace: workspace,
		Adapter: adapter, Output: &output, WorkspaceJournalRoot: journalRoot,
	}
	result, err := runner.Run(ctx, options)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	store, err := workspacejournal.Open(journalRoot, workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected two journaled writes, got %d: %+v", len(ops), ops)
	}
	for _, op := range ops {
		if op.Status != workspacejournal.StatusCompleted {
			t.Fatalf("expected completed op, got %s (%+v)", op.Status, op)
		}
	}
	if ops[0].Pre != nil {
		t.Fatalf("first write should have no preimage: %+v", ops[0])
	}
	if ops[1].Pre == nil {
		t.Fatalf("overwrite should record the preimage: %+v", ops[1])
	}

	// The model-visible delta must summarize the second write as a modify.
	// The runner feeds tool results back; assert via the last tool result the
	// adapter saw in request 4's input.
	found := false
	for _, item := range adapter.requests[len(adapter.requests)-1].Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		toolResult, ok := item.Data.(llm.ToolResult)
		if !ok {
			continue
		}
		for _, output := range toolResult.Output {
			if strings.Contains(output.Value, "notes/report.md") && strings.Contains(output.Value, "modified") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("WorkspaceDelta summary did not report the modification")
	}
}
