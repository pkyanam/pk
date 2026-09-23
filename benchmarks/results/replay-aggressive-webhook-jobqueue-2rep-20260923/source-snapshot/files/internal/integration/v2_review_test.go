package integration

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

// A task host must not advance its durable input cursor until the coordinator
// has persisted the input in the session. This also exercises stable-ID replay
// through the restored Inbox when a caller retries an already stored input.
func TestRunDurablyAcknowledgesSteeringInputAndDeduplicatesReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sessionDir, workspace := t.TempDir(), t.TempDir()
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	inputs := make(chan runner.Input, 1)
	const inputID = "task-input-0001"
	inputs <- runner.Input{ID: inputID, Text: "please keep going", Accepted: func(err error) { accepted <- err }}
	adapter := &scriptedAdapter{responses: []llm.Response{{
		ID: "steered-response", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "done"}}},
	}}}

	type runResult struct {
		id  string
		err error
	}
	resultCh := make(chan runResult, 1)
	go func() {
		result, runErr := runner.Run(ctx, runner.Options{
			Prompt: "start work", SessionDir: sessionDir, Workspace: workspace,
			Adapter: adapter, Store: store, Inputs: inputs, Output: io.Discard,
		})
		resultCh <- runResult{id: result.SessionID, err: runErr}
	}()

	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("steering input acceptance error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("steering input was not durably accepted")
	}
	// Closing the stream asks the long-lived coordinator to stop once idle.
	close(inputs)
	first := <-resultCh
	if first.err != nil {
		t.Fatalf("Run() error = %v", first.err)
	}

	state, err := store.Resume(ctx, session.ID(first.id))
	if err != nil {
		t.Fatalf("resume stored session: %v", err)
	}
	found := false
	for _, id := range state.ExternalInputIDs {
		if string(id) == inputID {
			found = true
		}
	}
	if !found {
		t.Fatalf("persisted external inputs %v do not contain %q", state.ExternalInputIDs, inputID)
	}

	// A second submission with the same ID on resume must not create another
	// user message; the upstream Inbox seeds its deduplication set from history.
	replayed := make(chan runner.Input, 1)
	replayed <- runner.Input{ID: inputID, Text: "please keep going"}
	close(replayed)
	second := &scriptedAdapter{responses: []llm.Response{{
		ID: "replay-response", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "continued"}}},
	}}}
	resumed, err := runner.Run(ctx, runner.Options{
		Prompt: "continue", SessionID: first.id, SessionDir: sessionDir,
		Workspace: workspace, Adapter: second, Store: store, Inputs: replayed,
	})
	if err != nil {
		t.Fatalf("resumed Run() error = %v", err)
	}
	if resumed.SessionID == "" {
		t.Fatal("resumed Run() returned an empty session ID")
	}
	second.mu.Lock()
	defer second.mu.Unlock()
	var replayCount int
	for _, item := range second.requests[0].Input {
		if item.Type != llm.ItemMessage {
			continue
		}
		message, ok := item.Data.(llm.Message)
		if ok && message.Role == llm.RoleUser && strings.Contains(message.Text, "please keep going") {
			replayCount++
		}
	}
	if replayCount != 1 {
		t.Errorf("steering message appeared %d times in resumed context, want exactly once", replayCount)
	}
}
