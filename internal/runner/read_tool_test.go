package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type readFixtureAdapter struct {
	calls   int
	result  string
	hasRead bool
}

func (a *readFixtureAdapter) Respond(ctx context.Context, req llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.calls++
	for _, tool := range req.Tools {
		if tool.Name == "Read" {
			a.hasRead = true
		}
	}
	if a.calls == 1 {
		return llm.Response{ID: "read-call", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "read-1", Name: "Read", Arguments: `{"path":"sample.txt","offset":2,"limit":1}`}}}}, nil
	}
	for _, item := range req.Input {
		if result, ok := item.Data.(llm.ToolResult); ok && result.CallID == "read-1" {
			for _, output := range result.Output {
				a.result += output.Value
			}
		}
	}
	return llm.Response{ID: "done", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Read complete."}}}}, nil
}
func TestReadToolRunsThroughDefaultRegistryAndJournalHandler(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.txt"), []byte("first\nunique second line\nthird\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	adapter := &readFixtureAdapter{}
	_, err := Run(ctx, Options{Prompt: "Read the second line", Workspace: workspace, SessionDir: t.TempDir(), WorkspaceJournalRoot: t.TempDir(), Adapter: adapter})
	if err != nil {
		t.Fatal(err)
	}
	if !adapter.hasRead || !strings.Contains(adapter.result, "unique second line") || strings.Contains(adapter.result, "third") {
		t.Fatalf("Read unavailable or incorrect: tools=%t result=%q", adapter.hasRead, adapter.result)
	}
	tools, _, err := PreviewToolCatalog(ctx, Options{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tools {
		if item.Name == "Read" && item.Source == "built-in" {
			return
		}
	}
	t.Fatal("Read missing from initial tool catalog")
}
