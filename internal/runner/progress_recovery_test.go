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

func TestUnfinishedProgressIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"The key evidence is in. Let me examine the heartbeat implementation and confirm the resend claim.", true},
		{"Let me check the tests.", true},
		{"I’ll inspect the runner now.", true},
		{"Done. All tests pass.", false},
		{"Would you like me to inspect it?", false},
		{"If you want, I’ll inspect it.", false},
		{"The docs say: Let me inspect it.", false},
		{strings.Repeat("x", 801), false},
	} {
		if got := unfinishedProgress(tc.text, ""); got != tc.want {
			t.Errorf("%q: %t", tc.text, got)
		}
	}
	if unfinishedProgress("Let me inspect it.", "final_answer") {
		t.Fatal("recovered explicit final")
	}
}

type progressFixtureAdapter struct {
	calls       int
	text        string
	sawRecovery bool
}

func (a *progressFixtureAdapter) Respond(_ context.Context, req llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.calls++
	for _, item := range req.Input {
		if m, ok := item.Data.(llm.Message); ok && strings.Contains(m.Text, "[pk runtime]") {
			a.sawRecovery = true
		}
	}
	if a.calls == 1 {
		return llm.Response{ID: "read", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "read-1", Name: "Read", Arguments: `{"path":"sample.txt"}`}}}}, nil
	}
	return llm.Response{ID: "progress", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: a.text}}}}, nil
}
func TestProgressRecoveryRunsOnceWithoutRestart(t *testing.T) {
	for _, tc := range []struct {
		text  string
		calls int
	}{{"Let me inspect the implementation next.", 3}, {"All requested checks passed.", 2}} {
		t.Run(tc.text, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "sample.txt"), []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			adapter := &progressFixtureAdapter{text: tc.text}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err := Run(ctx, Options{Workspace: workspace, SessionDir: t.TempDir(), Prompt: "Inspect and verify", Adapter: adapter, QueueInputs: true, Inputs: make(chan Input)})
			if err != nil {
				t.Fatal(err)
			}
			if adapter.calls != tc.calls || adapter.sawRecovery != (tc.calls == 3) {
				t.Fatalf("calls=%d recovery=%t", adapter.calls, adapter.sawRecovery)
			}
		})
	}
}
