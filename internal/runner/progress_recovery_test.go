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
		{"The merge completed cleanly — git auto-merged main.go. Let me verify the cases, then re-run the full verification suite:", true},
		{"Merge verified clean. Now the full verification suite on the merged tree, in parallel with the investigation:", true},
		{"The tag already exists. Let me just push it and create the release with the harness asset:", true},
		{"Now the true end-to-end proof: test self-update against the live release. I'll install a deliberately stale version, then self-update to v0.1.0 from GitHub:", true},
		{"I'll run the self-update test from /tmp/pk/v0.1.0/release.tar.gz now.", true},
		{"You're right — continuing: pushing the existing tag and creating the release:", true},
		{"Done. All tests pass.", false},
		{"The tag was pushed and the release was created successfully.", false},
		{"I installed v0.1.0 and verified the updater. The update is complete.", false},
		{"I'll run the updater test. The test passes and the release is complete.", false},
		{"The final URL is https://example.com/release/v0.1.0 and the release is live.", false},
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

func TestProgressRecoveryBudgetResetsOnlyForExternalTurns(t *testing.T) {
	for _, tc := range []struct {
		name             string
		external         bool
		internalRecovery bool
		wantReset        bool
	}{
		{name: "user input", external: true, wantReset: true},
		{name: "runtime recovery", external: true, internalRecovery: true},
		{name: "control input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sawToolWork, recovered := true, true
			resetProgressRecoveryForExternalInput(tc.external, tc.internalRecovery, &sawToolWork, &recovered)
			if (sawToolWork == false) != tc.wantReset || (recovered == false) != tc.wantReset {
				t.Fatalf("sawToolWork=%t recovered=%t, want reset=%t", sawToolWork, recovered, tc.wantReset)
			}
		})
	}
}

func TestProgressRecoveryRearmsAfterRealToolWork(t *testing.T) {
	sawToolWork, recovered := false, false
	resetProgressRecoveryForToolWork(true, &sawToolWork, &recovered)
	if !sawToolWork || recovered {
		t.Fatalf("real tool work should rearm recovery: tool=%t recovered=%t", sawToolWork, recovered)
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

type repeatedProgressAdapter struct{ calls, recoveryNotes int }

func (a *repeatedProgressAdapter) Respond(_ context.Context, req llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.calls++
	for _, item := range req.Input {
		if m, ok := item.Data.(llm.Message); ok && strings.Contains(m.Text, "[pk runtime]") {
			a.recoveryNotes++
		}
	}
	if a.calls == 1 || a.calls == 3 {
		id := "read-" + string(rune('0'+a.calls))
		return llm.Response{ID: id, Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: "Read", Arguments: `{"path":"sample.txt"}`}}}}, nil
	}
	text := "Now the true end-to-end proof: test self-update against the live release. I'll install a deliberately stale version, then self-update to v0.1.0 from GitHub:"
	if a.calls == 5 {
		text = "All requested checks passed."
	}
	return llm.Response{ID: "progress", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: text}}}}, nil
}

func TestProgressRecoveryRepeatsOnlyAfterRealToolWork(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	adapter := &repeatedProgressAdapter{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := Run(ctx, Options{Workspace: workspace, SessionDir: t.TempDir(), Prompt: "Inspect and verify", Adapter: adapter, QueueInputs: true, Inputs: make(chan Input)})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.calls != 5 || adapter.recoveryNotes < 2 {
		t.Fatalf("calls=%d recovery notes=%d, want 5 calls and two recoveries", adapter.calls, adapter.recoveryNotes)
	}
}
