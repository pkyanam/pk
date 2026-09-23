package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type testWriter struct{ lines chan []byte }

func (w *testWriter) Write(p []byte) (int, error) {
	w.lines <- append([]byte(nil), p...)
	return len(p), nil
}
func sendLine(t *testing.T, w io.Writer, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
}
func nextMessage(t *testing.T, w *testWriter) map[string]any {
	t.Helper()
	select {
	case line := <-w.lines:
		var message map[string]any
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatal(err)
		}
		return message
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ACP message")
		return nil
	}
}

func TestServerV1PromptSendsStreamingSessionUpdates(t *testing.T) {
	inR, inW := io.Pipe()
	out := &testWriter{lines: make(chan []byte, 16)}
	seen := make(chan Turn, 2)
	server, err := NewServer(inR, out, Config{Model: "gpt-6-luna", Effort: "medium", Run: func(_ context.Context, turn Turn, emit func(Update) error) (TurnResult, error) {
		seen <- turn
		if err := emit(Update{Kind: "assistant", MessageID: "resp-1", Text: "working"}); err != nil {
			return TurnResult{}, err
		}
		return TurnResult{SessionID: "durable-session"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	if got := nextMessage(t, out); got["id"].(float64) != 1 {
		t.Fatalf("initialize response=%v", got)
	}
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": "/tmp", "mcpServers": []any{}}})
	created := nextMessage(t, out)
	result := created["result"].(map[string]any)
	sessionID := result["sessionId"].(string)
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "hi"}}}})
	var sawUser, sawAssistant, sawResult bool
	for !sawResult {
		message := nextMessage(t, out)
		if message["method"] == "session/update" {
			params := message["params"].(map[string]any)
			update := params["update"].(map[string]any)
			switch update["sessionUpdate"] {
			case "user_message_chunk":
				sawUser = true
			case "agent_message_chunk":
				sawAssistant = update["content"].(map[string]any)["text"] == "working"
			}
		} else if message["id"] == float64(3) {
			sawResult = message["result"].(map[string]any)["stopReason"] == "end_turn"
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("missing updates user=%v assistant=%v", sawUser, sawAssistant)
	}
	firstTurn := <-seen
	if firstTurn.SessionID != "" || firstTurn.Prompt != "hi" || firstTurn.Workspace != "/tmp" || firstTurn.Model != "gpt-6-luna" {
		t.Fatalf("first turn=%+v", firstTurn)
	}
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/prompt", "params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "again"}}}})
	for {
		message := nextMessage(t, out)
		if message["id"] == float64(4) {
			break
		}
	}
	secondTurn := <-seen
	if secondTurn.SessionID != "durable-session" || secondTurn.Prompt != "again" {
		t.Fatalf("second turn=%+v", secondTurn)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServerAdvertisesAndLoadsSavedSessionWithOrderedReplay(t *testing.T) {
	inR, inW := io.Pipe()
	out := &testWriter{lines: make(chan []byte, 16)}
	const sessionID = "pk_durable-session"
	seen := make(chan Turn, 1)
	server, err := NewServer(inR, out, Config{
		LoadSession: func(_ context.Context, id, workspace string, emit func(Update) error) error {
			if id != sessionID || workspace != "/tmp" {
				t.Fatalf("load args id=%q workspace=%q", id, workspace)
			}
			if err := emit(Update{Kind: "user", MessageID: "old-user", Text: "prior question"}); err != nil {
				return err
			}
			return emit(Update{Kind: "assistant", MessageID: "old-assistant", Text: "prior answer"})
		},
		Run: func(_ context.Context, turn Turn, _ func(Update) error) (TurnResult, error) {
			seen <- turn
			return TurnResult{SessionID: turn.SessionID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	initialized := nextMessage(t, out)
	caps := initialized["result"].(map[string]any)["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != true {
		t.Fatalf("initialize capabilities=%v", caps)
	}
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/load", "params": map[string]any{"sessionId": sessionID, "cwd": "/tmp", "mcpServers": []any{}}})
	var replay []string
	for {
		message := nextMessage(t, out)
		if method, _ := message["method"].(string); method == "session/update" {
			params := message["params"].(map[string]any)
			if params["sessionId"] != sessionID {
				t.Fatalf("replay session=%v", params)
			}
			update := params["update"].(map[string]any)
			replay = append(replay, update["sessionUpdate"].(string))
		} else if message["id"] == float64(2) {
			if _, ok := message["result"].(map[string]any); !ok {
				t.Fatalf("load response=%v", message)
			}
			break
		}
	}
	if len(replay) != 2 || replay[0] != "user_message_chunk" || replay[1] != "agent_message_chunk" {
		t.Fatalf("replay sequence=%v", replay)
	}
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "continue"}}}})
	for {
		message := nextMessage(t, out)
		if message["id"] == float64(3) {
			break
		}
	}
	turn := <-seen
	if turn.SessionID != sessionID || turn.Prompt != "continue" {
		t.Fatalf("loaded turn=%+v", turn)
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServerCancelReturnsCancelledStopReason(t *testing.T) {
	inR, inW := io.Pipe()
	out := &testWriter{lines: make(chan []byte, 16)}
	started := make(chan struct{})
	server, err := NewServer(inR, out, Config{Run: func(ctx context.Context, _ Turn, _ func(Update) error) (TurnResult, error) {
		close(started)
		<-ctx.Done()
		return TurnResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background()) }()
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	_ = nextMessage(t, out)
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": "/tmp"}})
	created := nextMessage(t, out)
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "wait"}}}})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not start")
	}
	sendLine(t, inW, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": sessionID}})
	for {
		message := nextMessage(t, out)
		if message["id"] == float64(3) {
			if message["result"].(map[string]any)["stopReason"] != "cancelled" {
				t.Fatalf("prompt response=%v", message)
			}
			break
		}
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPromptBlocksRejectUnsupportedInputAndJoinSupportedText(t *testing.T) {
	blocks := []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`), json.RawMessage(`{"type":"resource_link","name":"guide","uri":"file:///tmp/guide.txt"}`)}
	text, err := promptText(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "hello") || !strings.Contains(text, "file:///tmp/guide.txt") {
		t.Fatalf("prompt text=%q", text)
	}
	_, err = promptText([]json.RawMessage{json.RawMessage(`{"type":"image"}`)})
	if err == nil {
		t.Fatal("expected unsupported image block to be rejected")
	}
}
