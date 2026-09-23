package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
