package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestRunBuildsCodingPromptFromWorkspaceAndUserInstructions(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("Follow this repository's local convention."), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{{
		ID: "done", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "ready"}}},
	}}}
	_, err := runner.Run(ctx, runner.Options{
		Prompt: "inspect", SystemPrompt: "Keep output concise.", SessionDir: t.TempDir(), Workspace: workspace, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 1 || len(adapter.requests[0].Input) == 0 {
		t.Fatalf("requests = %#v", adapter.requests)
	}
	message, ok := adapter.requests[0].Input[0].Data.(llm.Message)
	if !ok || message.Role != llm.RoleSystem {
		t.Fatalf("first item = %#v, want system message", adapter.requests[0].Input[0])
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	for _, text := range []string{"You are pk", workspace, platform, "AGENTS.md", "Follow this repository's local convention.", "Keep output concise.", "nearest nested AGENTS.md"} {
		if !strings.Contains(message.Text, text) {
			t.Errorf("system message omitted %q", text)
		}
	}
}

func TestWorkspaceInstructionsOversizeIsBoundedAndWarned(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "AGENTS.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 70<<10)), 0o600); err != nil {
		t.Fatal(err)
	}
	var diagnostics strings.Builder
	adapter := &scriptedAdapter{responses: []llm.Response{{
		ID: "done", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "ready"}}},
	}}}
	_, err := runner.Run(t.Context(), runner.Options{
		Prompt: "inspect", SystemPrompt: "extra", SessionDir: t.TempDir(), Workspace: workspace,
		Adapter: adapter, Diagnostics: &diagnostics,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "exceeds") {
		t.Fatalf("diagnostics = %q, want oversize warning", diagnostics.String())
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	message := adapter.requests[0].Input[0].Data.(llm.Message)
	if strings.Contains(message.Text, strings.Repeat("x", 256)) {
		t.Fatal("oversized AGENTS.md was included in the prompt")
	}
	if !strings.Contains(message.Text, "extra") {
		t.Fatal("explicit system instructions were lost")
	}
}
