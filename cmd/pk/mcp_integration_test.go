package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestMain(m *testing.M) {
	if os.Getenv("PK_TEST_MCP_SERVER") == "1" {
		server := mcp.NewServer(&mcp.Implementation{Name: "pk-mcp-cli-fixture", Version: "1"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo text", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"},
		}}, func(_ context.Context, _ *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
			value, _ := input["text"].(string)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture:" + value}}}, nil, nil
		})
		if server.Run(context.Background(), &mcp.StdioTransport{}) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRPCMCPToolRuntimeAndSavedSchemaMismatch(t *testing.T) {
	home, workspace, sessions := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PK_HOME", home)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := mcpclient.ServerConfig{ID: "rpcfixture", Command: executable, Args: []string{"-test.run=^TestMain$"}, Env: map[string]string{"PK_TEST_MCP_SERVER": "1"}}
	store := mcpclient.ConfigStore{Home: home}
	if err := store.Add(config); err != nil {
		t.Fatal(err)
	}

	// Resolve the deterministic exported name used by the actual RPC runner.
	probeOptions := runner.Options{Workspace: workspace, SessionDir: sessions}
	probe, err := configureCLIMCP(context.Background(), &probeOptions, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var exportedName string
	for _, tool := range probe.Tools() {
		if tool.ServerToolName == "echo" {
			exportedName = tool.Name
		}
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	if exportedName == "" {
		t.Fatal("fixture tool was not exported")
	}

	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "rpc-mcp-call", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "rpc-mcp-1", Name: exportedName, Arguments: `{"text":"rpc"}`}}}}},
		{response: llm.Response{ID: "rpc-mcp-done", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "MCP returned fixture:rpc"}}}}},
	}}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, sessionDir: sessions, started: true,
		opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: adapter, requestTypes: map[string]string{}}

	// Cataloging is configuration-only and doesn't start the worker.
	server.handle(rpcMessage{Version: 1, ID: "mcp-list", Type: "mcp_list"}, make(chan turnDone, 1))
	var listed rpcEvent
	if err := json.Unmarshal(<-sink.events, &listed); err != nil || listed.Type != "mcp_catalog" {
		t.Fatalf("MCP list event=%+v err=%v", listed, err)
	}

	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "rpc-prompt", Type: "prompt", Payload: json.RawMessage(`{"text":"use the configured echo tool"}`)}, finished)
	var first turnDone
	select {
	case first = <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("RPC MCP prompt timed out")
	}
	if first.err != nil || !strings.Contains(first.text, "fixture:rpc") {
		t.Fatalf("RPC result=%+v", first)
	}
	server.completeTurn(first)
	if server.session == "" {
		t.Fatal("RPC did not persist its session")
	}

	// A changed server/tool set must be rejected by the saved session before
	// another request reaches the model adapter.
	if err := store.Remove(config.ID); err != nil {
		t.Fatal(err)
	}
	model.mu.Lock()
	callsBefore := model.calls
	model.mu.Unlock()
	finished = make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "rpc-stale", Type: "prompt", Payload: json.RawMessage(`{"text":"continue"}`)}, finished)
	select {
	case stale := <-finished:
		if stale.err == nil || !strings.Contains(strings.ToLower(stale.err.Error()), "mcp") {
			t.Fatalf("changed MCP schema error=%v", stale.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stale-schema RPC prompt timed out")
	}
	model.mu.Lock()
	callsAfter := model.calls
	model.mu.Unlock()
	if callsAfter != callsBefore {
		t.Fatalf("model called on stale tool schema: before=%d after=%d", callsBefore, callsAfter)
	}
}

// This exercises the same CLI setup path used by pk run: an explicitly
// configured stdio server is discovered, added to the model registry, invoked
// through the async operation handler, and its result returned to the model.
func TestConfiguredMCPToolRunsThroughCLIRunner(t *testing.T) {
	home, workspace, sessions := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PK_HOME", home)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := mcpclient.ConfigStore{Home: home}
	if err := store.Add(mcpclient.ServerConfig{ID: "fixture", Command: executable, Args: []string{"-test.run=^TestMain$"}, Env: map[string]string{"PK_TEST_MCP_SERVER": "1"}}); err != nil {
		t.Fatal(err)
	}
	options := runner.Options{Prompt: "call echo", Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low", Output: io.Discard}
	host, err := configureCLIMCP(context.Background(), &options, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if host == nil || options.RemoteJobHandlers == nil || options.DecorateRegistry == nil || options.MCPFingerprint == "" {
		t.Fatalf("MCP setup missing runtime pieces: host=%v handler=%v decorator=%v fingerprint=%q", host != nil, options.RemoteJobHandlers != nil, options.DecorateRegistry != nil, options.MCPFingerprint)
	}
	defer host.Close()
	var exportedName string
	for _, tool := range host.Tools() {
		if tool.ServerToolName == "echo" {
			exportedName = tool.Name
		}
	}
	if exportedName == "" {
		t.Fatal("fixture echo tool was not discovered")
	}
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "mcp-tool-call", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "mcp-call", Name: exportedName, Arguments: `{"text":"hello"}`}}}}},
		{response: llm.Response{ID: "mcp-finish", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "The server replied fixture:hello."}}}}},
	}}
	options.Adapter = model
	result, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "fixture:hello") || model.calls != 2 {
		t.Fatalf("MCP runner result=%q model calls=%d", result.Text, model.calls)
	}
	rpc := &rpcServer{sessionDir: sessions, session: result.SessionID, opts: runner.Options{Workspace: workspace}}
	toolCatalog, err := rpc.modelToolCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !toolCatalog.Saved || !containsSavedTool(toolCatalog.Tools, exportedName) {
		t.Fatalf("saved model tool catalog = %#v", toolCatalog)
	}
	mcpCatalog, err := rpc.mcpCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !mcpCatalog.SavedSessionTools || !mcpCatalog.NextSessionOnly || len(mcpCatalog.Tools) != 1 || mcpCatalog.Tools[0].Name != exportedName {
		t.Fatalf("saved MCP catalog = %#v", mcpCatalog)
	}
}

func containsSavedTool(tools []runner.SavedToolSummary, name string) bool {
	for _, item := range tools {
		if item.Name == name {
			return true
		}
	}
	return false
}
