package runner

import (
	"path/filepath"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

func TestLoadSavedMCPToolsUsesSessionSnapshotWithoutDiscovery(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	workspace := filepath.Join(root, "workspace")
	snapshot := ContextSnapshot{Version: contextSnapshotVersion, Workspace: workspace, Tools: []llm.Tool{
		{Type: llm.ToolFunction, Name: "mcp_files_search_1234abcd", Description: "frozen schema", Parameters: map[string]any{"type": "object", "required": []any{"query"}}},
		{Type: llm.ToolFunction, Name: "Bash", Description: "built-in"},
	}}
	store := fileContextSnapshotStore{directory: sessionDir}
	if err := store.SaveContext(ctx, session.ID("mcp-catalog"), snapshot); err != nil {
		t.Fatal(err)
	}
	tools, saved, err := LoadSavedMCPTools(ctx, sessionDir, "mcp-catalog", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !saved || len(tools) != 1 || tools[0].Name != "mcp_files_search_1234abcd" || tools[0].Description != "frozen schema" {
		t.Fatalf("saved MCP tool catalog = %#v, saved=%v", tools, saved)
	}
	if _, saved, err := LoadSavedMCPTools(ctx, sessionDir, "missing", workspace); err != nil || saved {
		t.Fatalf("missing snapshot = saved %v, err %v", saved, err)
	}
	summaries, saved, err := LoadSavedToolCatalog(ctx, sessionDir, "mcp-catalog", workspace)
	if err != nil || !saved || len(summaries) != 2 || summaries[0].Source != "MCP" || summaries[1].Source != "built-in" {
		t.Fatalf("saved tool summaries = %#v saved=%v err=%v", summaries, saved, err)
	}
}

func TestMCPFingerprintPreservesLegacyOnlyWhenNoMCPIsConfigured(t *testing.T) {
	legacy := ContextSnapshot{}
	if err := validateMCPFingerprint(legacy, ""); err != nil {
		t.Fatalf("unchanged legacy resume rejected: %v", err)
	}
	if err := validateMCPFingerprint(legacy, "configured-mcp-fingerprint"); err == nil {
		t.Fatal("legacy session accepted a newly configured MCP toolset")
	}
	saved := ContextSnapshot{MCPFingerprint: "saved-fingerprint"}
	if err := validateMCPFingerprint(saved, "saved-fingerprint"); err != nil {
		t.Fatalf("same MCP fingerprint rejected: %v", err)
	}
	if err := validateMCPFingerprint(saved, "changed-fingerprint"); err == nil {
		t.Fatal("changed MCP fingerprint accepted")
	}
}
