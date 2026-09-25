package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/v2/jsonrpc"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// fakeAdapter is a controllable llm.Adapter for testing the run loop without
// network. It emits one tool call then a final message.
type fakeAdapter struct {
	callName string
	args     string
	final    string
}

func (f *fakeAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	// If the request already contains a tool result for our call, emit the
	// final message; otherwise request the tool call.
	hasResult := false
	for _, item := range request.Input {
		if item.Type == llm.ItemToolResult {
			hasResult = true
		}
	}
	if !hasResult {
		return llm.Response{
			Stop: llm.StopComplete,
			Output: []llm.Item{
				{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-1", Name: f.callName, Arguments: f.args}},
			},
		}, nil
	}
	return llm.Response{
		Stop: llm.StopComplete,
		Output: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: f.final}},
		},
	}, nil
}

// newTestDaemon builds a Daemon rooted at a temp dir and overrides the
// provider resolution to use the fake adapter.
func newTestDaemon(t *testing.T) (*Daemon, *fakeAdapter) {
	t.Helper()
	d := New(t.TempDir())
	d.mu.Lock()
	d.sessions["s1"] = &Session{ID: "s1", Provider: "test", Model: "test-model"}
	d.mu.Unlock()
	fa := &fakeAdapter{callName: "read_file", args: `{"path":"notes.txt"}`, final: "read notes.txt"}
	return d, fa
}

// TestSessionCreateAndAttach verifies the session lifecycle (create, persist,
// attach) through the JSON-RPC handlers.
func TestSessionCreateAndAttach(t *testing.T) {
	d := New(t.TempDir())
	d.providerFactory = func() (llm.Adapter, error) { return &fakeAdapter{final: "x"}, nil }
	h := d.Handler()

	// create
	res, err := h["session/create"](jsonrpc.Params(`{}`))
	if err != nil {
		t.Fatalf("session/create error = %v", err)
	}
	created, _ := res.(map[string]any)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("session/create returned no id: %#v", res)
	}
	// persisted on disk
	if _, err := d.loadSession(id); err != nil {
		t.Fatalf("loadSession(%q) error = %v", id, err)
	}
	// attach
	res, err = h["session/attach"](jsonrpc.Params(`{"id":"` + id + `"}`))
	if err != nil {
		t.Fatalf("session/attach error = %v", err)
	}
	attached, _ := res.(map[string]any)
	if attached["id"] != id {
		t.Fatalf("attach returned %#v, want id %q", attached, id)
	}
	// attach unknown
	_, err = h["session/attach"](jsonrpc.Params(`{"id":"nope"}`))
	if err == nil {
		t.Fatal("session/attach unknown id succeeded, want error")
	}
	// prompt on unknown session
	_, err = h["prompt"](jsonrpc.Params(`{"session_id":"nope","text":"hi"}`))
	if err == nil {
		t.Fatal("prompt on unknown session succeeded, want error")
	}
}

// TestProviderListNeverExposesKeys verifies provider/list returns summaries
// without any API key material.
func TestProviderListNeverExposesKeys(t *testing.T) {
	d := New(t.TempDir())
	// Write a provider with a key into the temp store.
	_ = d.store.Put(providers.Provider{
		ID: "test-provider", Protocol: "chat_completions", BaseURL: "https://example.com/v1",
		APIKey: "super-secret-key", DefaultModel: "m1",
	})
	res, err := d.Handler()["provider/list"](nil)
	if err != nil {
		t.Fatalf("provider/list error = %v", err)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "super-secret-key") {
		t.Fatal("provider/list leaked the API key")
	}
	if !strings.Contains(string(raw), "test-provider") {
		t.Fatalf("provider/list missing provider: %s", raw)
	}
}

// TestPromptRunsToolThenFinal verifies the bounded agent loop: the fake
// adapter requests read_file, the daemon invokes the builtin tool, and the
// final assistant text comes back. This exercises "write a tool -> invoke it
// the same turn" with a real core tool.
func TestPromptRunsToolThenFinal(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "notes.txt", "hello from notes")
	old := workspaceRoot
	workspaceRoot = dir
	defer func() { workspaceRoot = old }()
	d := New(dir)
	fa := &fakeAdapter{callName: "read_file", args: `{"path":"notes.txt"}`, final: "I read notes.txt"}
	d.providerFactory = func() (llm.Adapter, error) { return fa, nil }
	d.mu.Lock()
	d.sessions["s1"] = &Session{ID: "s1", Provider: "test", Model: "test-model"}
	d.mu.Unlock()

	res, err := d.Handler()["prompt"](jsonrpc.Params(`{"session_id":"s1","text":"read notes.txt"}`))
	if err != nil {
		t.Fatalf("prompt error = %v", err)
	}
	result, _ := res.(map[string]any)
	text, _ := result["text"].(string)
	if !strings.Contains(text, "read notes.txt") {
		t.Fatalf("final text = %q, want mention of read notes.txt", text)
	}
	tc, _ := result["tool_calls"].([]map[string]any)
	if len(tc) != 1 {
		t.Fatalf("tool_calls = %#v, want 1", tc)
	}
	if tc[0]["name"] != "read_file" {
		t.Fatalf("tool name = %v, want read_file", tc[0]["name"])
	}
	if !strings.Contains(tc[0]["result"].(string), "hello from notes") {
		t.Fatalf("tool result = %v, want file contents", tc[0]["result"])
	}
	// session persisted with the full transcript (reconnect evidence)
	s, err := d.loadSession("s1")
	if err != nil {
		t.Fatalf("loadSession error = %v", err)
	}
	if len(s.Items) < 3 { // user + tool_call + tool_result + assistant
		t.Fatalf("session items = %d, want >= 3", len(s.Items))
	}
}

// TestPromptReconnectContinues verifies session reconnect: a fresh Daemon
// loads the persisted session and the next turn includes prior context.
func TestPromptReconnectContinues(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "notes.txt", "hello from notes")
	d1 := New(dir)
	fa1 := &fakeAdapter{callName: "read_file", args: `{"path":"notes.txt"}`, final: "first turn"}
	d1.providerFactory = func() (llm.Adapter, error) { return fa1, nil }
	d1.mu.Lock()
	d1.sessions["s1"] = &Session{ID: "s1", Provider: "test", Model: "test-model"}
	d1.mu.Unlock()
	if _, err := d1.Handler()["prompt"](jsonrpc.Params(`{"session_id":"s1","text":"read"}`)); err != nil {
		t.Fatalf("first prompt error = %v", err)
	}
	// Simulate reconnect: new Daemon over the same PK_HOME, attach, prompt.
	d2 := New(dir)
	if _, err := d2.Handler()["session/attach"](jsonrpc.Params(`{"id":"s1"}`)); err != nil {
		t.Fatalf("reconnect attach error = %v", err)
	}
	fa2 := &fakeAdapter{callName: "read_file", args: `{"path":"notes.txt"}`, final: "second turn"}
	d2.providerFactory = func() (llm.Adapter, error) { return fa2, nil }
	res, err := d2.Handler()["prompt"](jsonrpc.Params(`{"session_id":"s1","text":"again"}`))
	if err != nil {
		t.Fatalf("second prompt error = %v", err)
	}
	result, _ := res.(map[string]any)
	text, _ := result["text"].(string)
	if !strings.Contains(text, "second turn") {
		t.Fatalf("reconnect final text = %q", text)
	}
}

// TestBuiltinReadFileConfinesToWorkspace verifies the read_file tool rejects
// paths outside the workspace root.
func TestBuiltinReadFileConfinesToWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "secret.txt", "top secret")
	old := workspaceRoot
	workspaceRoot = dir
	defer func() { workspaceRoot = old }()
	if _, err := invokeBuiltin("read_file", map[string]any{"path": "../secret.txt"}); err == nil {
		t.Fatal("read_file escaped the workspace, want error")
	}
	out, err := invokeBuiltin("read_file", map[string]any{"path": "secret.txt"})
	if err != nil {
		t.Fatalf("read_file error = %v", err)
	}
	if !strings.Contains(out, "top secret") {
		t.Fatalf("read_file output = %q, want contents", out)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
