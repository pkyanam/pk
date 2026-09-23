package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/acp"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestACPCommandRunsPromptThroughRunnerWithInjectedAdapter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PK_HOME", filepath.Join(home, ".pk"))
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "reply-1", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "ACP works"}}}}}}}
	adapter := func(context.Context) (*codexAdapter, error) {
		return &codexAdapter{client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, nil
	}
	diagnostics := &strings.Builder{}
	exit := make(chan int, 1)
	go func() {
		exit <- runACPCommandWithAdapter(context.Background(), []string{"--model", "test-model"}, inR, outW, diagnostics, adapter)
	}()
	scanner := bufio.NewScanner(outR)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(inW, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("read ACP response: %v", scanner.Err())
		}
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	if got := next(); got["id"].(float64) != 1 {
		t.Fatalf("initialize response=%v", got)
	}
	workspace := filepath.Join(home, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": workspace, "mcpServers": []any{}}})
	created := next()
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "say ACP works"}}}})
	updates := []string{}
	for {
		message := next()
		if method, ok := message["method"].(string); ok && method == "session/update" {
			update := message["params"].(map[string]any)["update"].(map[string]any)
			updates = append(updates, update["sessionUpdate"].(string))
			if update["sessionUpdate"] == "agent_message_chunk" && update["content"].(map[string]any)["text"] != "ACP works" {
				t.Fatalf("assistant update=%v", update)
			}
			continue
		}
		if message["id"] == float64(3) {
			if message["result"].(map[string]any)["stopReason"] != "end_turn" {
				t.Fatalf("prompt response=%v", message)
			}
			break
		}
	}
	if len(updates) < 2 || updates[0] != "user_message_chunk" {
		t.Fatalf("session update sequence=%v", updates)
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 1 {
		t.Fatalf("mock adapter calls=%d", calls)
	}
	_ = inW.Close()
	_ = outW.Close()
	select {
	case code := <-exit:
		if code != 0 {
			t.Fatalf("ACP command exit=%d diagnostics=%s", code, diagnostics.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ACP command did not exit")
	}
}

func TestACPReplayLoadsRunnerSessionAndChecksWorkspace(t *testing.T) {
	workspace := t.TempDir()
	sessions := filepath.Join(t.TempDir(), "sessions")
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "history-answer", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "saved answer"}}}}}}}
	result, err := runner.Run(context.Background(), runner.Options{Prompt: "saved question", Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium", Adapter: model})
	if err != nil {
		t.Fatal(err)
	}
	var updates []acp.Update
	err = replayACPSession(context.Background(), sessions, result.SessionID, workspace, func(update acp.Update) error {
		updates = append(updates, update)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) < 2 || updates[0].Kind != "user" || updates[0].Text != "saved question" || updates[1].Kind != "assistant" || updates[1].Text != "saved answer" {
		t.Fatalf("replayed updates=%+v", updates)
	}
	if err := replayACPSession(context.Background(), sessions, result.SessionID, t.TempDir(), func(acp.Update) error { return nil }); err == nil {
		t.Fatal("loaded session into a different workspace")
	}
}
