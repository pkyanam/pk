package runner

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type gitSnapshotCapture struct{ system string }

func (a *gitSnapshotCapture) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Role == llm.RoleSystem {
			a.system = message.Text
		}
	}
	return llm.Response{ID: "git-snapshot", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "done"}}}}, nil
}

func TestGitWorkspaceChangePreservesSavedPrefix(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	workspace := t.TempDir()
	sessions := filepath.Join(t.TempDir(), "sessions")
	first := &gitSnapshotCapture{}
	result, err := Run(t.Context(), Options{Prompt: "hello", Workspace: workspace, SessionDir: sessions, Adapter: first})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "--quiet", workspace)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	resumed := &gitSnapshotCapture{}
	if _, err := Run(t.Context(), Options{Prompt: "continue", SessionID: result.SessionID, Workspace: workspace, SessionDir: sessions, Adapter: resumed}); err != nil {
		t.Fatal(err)
	}
	if first.system == "" || resumed.system != first.system {
		t.Fatal("resuming rewrote the saved system prefix after git init")
	}
	fresh := &gitSnapshotCapture{}
	if _, err := Run(t.Context(), Options{Prompt: "hello", Workspace: workspace, SessionDir: sessions, Adapter: fresh}); err != nil {
		t.Fatal(err)
	}
	if fresh.system == first.system {
		t.Fatal("new session did not refresh Git workspace context")
	}
}
