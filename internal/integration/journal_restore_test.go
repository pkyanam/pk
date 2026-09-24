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

// TestJournalRestoreRevertsWorkspaceAndRefusesConflicts covers the restore
// contract end to end: a journaled write is reverted, user edits survive
// conflicts, and every restore leaves a safety snapshot.
func TestJournalRestoreRevertsWorkspaceAndRefusesConflicts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	workspace := t.TempDir()
	journalRoot := filepath.Join(t.TempDir(), "journal")
	if err := os.MkdirAll(filepath.Join(workspace, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	create, _ := json.Marshal(map[string]any{"path": "notes/report.md", "content": "alpha\nbeta\n"})
	overwrite, _ := json.Marshal(map[string]any{"path": "notes/report.md", "content": "alpha\ngamma\n", "overwrite": true})
	other, _ := json.Marshal(map[string]any{"path": "notes/other.md", "content": "other\n"})
	final := llm.Response{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}}}
	adapter := &scriptedAdapter{responses: []llm.Response{
		{ID: "t1", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "c1", Name: "WriteFile", Arguments: string(create)}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "c2", Name: "WriteFile", Arguments: string(other)}},
		}},
		{ID: "t2", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "c3", Name: "WriteFile", Arguments: string(overwrite)}}}},
		final,
	}}
	var output strings.Builder
	result, err := runner.Run(ctx, runner.Options{Prompt: "x", SessionDir: t.TempDir(), Workspace: workspace, Adapter: adapter, Output: &output, WorkspaceJournalRoot: journalRoot})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	current, err := os.ReadFile(filepath.Join(workspace, "notes", "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "alpha\ngamma\n" {
		t.Fatalf("unexpected workspace state: %q", current)
	}

	store, err := workspacejournal.Open(journalRoot, workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	// User edits the file after the agent finishes; restore must refuse.
	if err := os.WriteFile(filepath.Join(workspace, "notes", "report.md"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict, err := store.Restore(ctx, result.SessionID, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflict.Restored) != 0 {
		t.Fatalf("conflicting state must not restore: %+v", conflict)
	}
	current, err = os.ReadFile(filepath.Join(workspace, "notes", "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "user edit\n" {
		t.Fatalf("user edit must survive a refused restore: %q", current)
	}

	// Restoring only the superseded first write is allowed once the file
	// matches the state the second write recorded: the second op's expected
	// state is "alpha\ngamma\n" and the first op has no preimage, so select
	// the superseding op explicitly after resetting the file.
	if err := os.WriteFile(filepath.Join(workspace, "notes", "report.md"), []byte("alpha\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var overwriteID string
	for _, op := range ops {
		if op.Path == "notes/report.md" && op.Pre != nil {
			overwriteID = op.ID
		}
	}
	report, err := store.Restore(ctx, result.SessionID, workspace, []string{overwriteID})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Restored) != 1 {
		t.Fatalf("expected the overwrite to restore: %+v", report)
	}
	current, err = os.ReadFile(filepath.Join(workspace, "notes", "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "alpha\nbeta\n" {
		t.Fatalf("expected the recorded preimage, got %q", current)
	}
	if report.SnapshotDir == "" {
		t.Fatal("restore must keep a safety snapshot")
	}
	if _, err := os.Stat(filepath.Join(report.SnapshotDir, "snapshot.json")); err != nil {
		t.Fatalf("snapshot manifest missing: %v", err)
	}
}
