package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestMain(m *testing.M) {
	if os.Getenv("PK_MCPCLIENT_FIXTURE") == "1" {
		os.Exit(runFixtureServer())
	}
	os.Exit(m.Run())
}

func runFixtureServer() int {
	if exitFile := os.Getenv("PK_MCPCLIENT_EXIT_FILE"); exitFile != "" {
		defer func() { _ = os.WriteFile(exitFile, []byte("exited\n"), 0o600) }()
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "pk-mcp-test", Version: "1.0.0"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo one string", InputSchema: schema}, func(_ context.Context, _ *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, any, error) {
		text, _ := input["text"].(string)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + text}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "slow", Description: "Wait until canceled", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	mcp.AddTool(server, &mcp.Tool{Name: "pwd", Description: "Return process directory", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: cwd}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return 1
	}
	return 0
}

func fixtureConfig(t *testing.T) ServerConfig {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ServerConfig{ID: "test", Command: executable, Args: []string{"-test.run=^TestMain$"}, Env: map[string]string{"PK_MCPCLIENT_FIXTURE": "1"}}
}

func TestStdioHostDiscoversCallsAndClosesTrustedFixture(t *testing.T) {
	ctx := t.Context()
	host, report, err := NewHost(ctx, []ServerConfig{fixtureConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || report.Loaded[0] != "test" || len(report.Warnings) != 0 {
		t.Fatalf("server report = %#v", report)
	}
	tools := host.Tools()
	if len(tools) != 3 {
		t.Fatalf("tools = %#v", tools)
	}
	var echo Tool
	for _, item := range tools {
		if item.ServerToolName == "echo" {
			echo = item
		}
		if len(item.Name) > maxExportedToolName || !strings.HasPrefix(item.Name, "mcp_test_") {
			t.Fatalf("exported tool name = %q", item.Name)
		}
	}
	if echo.Name == "" || echo.InputSchema["type"] != "object" {
		t.Fatalf("echo tool = %#v", echo)
	}
	result, err := host.call(ctx, echo.Name, map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "echo:hello" {
		t.Fatalf("MCP call result = %#v", result)
	}
	var pwdName string
	for _, item := range tools {
		if item.ServerToolName == "pwd" {
			pwdName = item.Name
		}
	}
	pwd, err := host.call(ctx, pwdName, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	processDir := pwd.Content[0].(*mcp.TextContent).Text
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(processDir) != filepath.Clean(home) {
		t.Fatalf("default MCP working directory = %q, want home %q", processDir, home)
	}
}

func TestStdioCallHonorsContextCancellation(t *testing.T) {
	host, _, err := NewHost(t.Context(), []ServerConfig{fixtureConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	var name string
	for _, item := range host.Tools() {
		if item.ServerToolName == "slow" {
			name = item.Name
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = host.call(ctx, name, map[string]any{})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled MCP call error = %v, want deadline exceeded", err)
	}
}

func TestHostCloseReapsStdioServerProcess(t *testing.T) {
	exitFile := filepath.Join(t.TempDir(), "server-exited")
	config := fixtureConfig(t)
	config.Env["PK_MCPCLIENT_EXIT_FILE"] = exitFile
	host, report, err := NewHost(t.Context(), []ServerConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Loaded) != 1 {
		t.Fatalf("fixture server did not connect: %+v", report)
	}
	var echoName string
	for _, item := range host.Tools() {
		if item.ServerToolName == "echo" {
			echoName = item.Name
		}
	}
	if echoName == "" {
		t.Fatal("connected fixture did not expose echo tool")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(exitFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stdio MCP fixture did not exit after Host.Close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := host.call(t.Context(), echoName, map[string]any{"text": "after close"}); err == nil {
		t.Fatal("closed host still accepted tool calls")
	}
}

func TestRemoteHandlerCancellationDrainsCallBeforeHostClose(t *testing.T) {
	exitFile := filepath.Join(t.TempDir(), "server-exited")
	config := fixtureConfig(t)
	config.Env["PK_MCPCLIENT_EXIT_FILE"] = exitFile
	host, report, err := NewHost(t.Context(), []ServerConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Loaded) != 1 {
		t.Fatalf("fixture server did not connect: %+v", report)
	}
	var slow Tool
	for _, item := range host.Tools() {
		if item.ServerToolName == "slow" {
			slow = item
		}
	}
	if slow.Name == "" {
		t.Fatal("connected fixture did not expose slow tool")
	}
	decorated, issues := DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), host)
	if len(issues) != 0 {
		t.Fatalf("decorate issues=%v", issues)
	}
	translator, ok := decorated.Resolve(slow.Name)
	if !ok {
		t.Fatalf("registry did not expose %q", slow.Name)
	}
	callContext := &submitContext{}
	status := translator.Translate(callContext, llm.ToolCall{CallID: "cancel-me", Name: slow.Name, Arguments: `{}`})
	if status.Error != "" || len(status.WaitingFor) != 1 {
		t.Fatalf("slow tool status=%+v", status)
	}

	handlerCtx, cancel := context.WithCancel(t.Context())
	handler := host.RemoteJobHandlers(handlerCtx)[0]
	defer cancel()
	op := operation.Operation{ID: "mcp-slow", Type: callContext.spec.Type, Version: callContext.spec.Version, Status: operation.StatusReady, State: callContext.spec.State, MaxOutputLength: callContext.spec.MaxOutputLength}
	if err := handler.AddRemoteJob(op); err != nil {
		t.Fatal(err)
	}
	// Consume the initial Awaiting update so cancellation cannot block on the
	// bounded update channel while the runner is tearing down.
	select {
	case <-handler.RemoteJobUpdates():
	case <-time.After(3 * time.Second):
		t.Fatal("remote job did not enter the awaiting state")
	}
	cancel()
	waited := make(chan struct{})
	go func() { handler.(interface{ Wait() }).Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("remote handler did not drain the canceled MCP call")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(exitFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("MCP server process remained after canceled turn teardown")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExplicitConfigValidationAndEnvironmentFiltering(t *testing.T) {
	config := fixtureConfig(t)
	config.Command = "relative-server"
	if err := config.validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative command validation error = %v", err)
	}
	t.Setenv("PK_MCPCLIENT_UNSELECTED_SECRET", "do-not-pass")
	env := processEnvironment(map[string]string{"PK_MCPCLIENT_SELECTED": "selected"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "PK_MCPCLIENT_UNSELECTED_SECRET") || !strings.Contains(joined, "PK_MCPCLIENT_SELECTED=selected") {
		t.Fatalf("MCP child environment has unexpected selection: %q", joined)
	}
}

func TestExportNamesAreBoundedStableAndNamespaced(t *testing.T) {
	first := exportName("server", "a/tool with a very long unusual name 💥")
	second := exportName("server", "a/tool with a very long unusual name 💥")
	if first != second || len(first) > maxExportedToolName || !strings.HasPrefix(first, "mcp_server_") {
		t.Fatalf("export names = %q and %q", first, second)
	}
	if first == exportName("other", "a/tool with a very long unusual name 💥") {
		t.Fatal("different server IDs exported the same name")
	}
}

func TestNonTextMCPContentIsExplicitlyReportedAndOutputIsBounded(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: strings.Repeat("x", maxToolResult)},
		&mcp.ImageContent{MIMEType: "image/png", Data: []byte("not forwarded")},
	}}
	outputs := contentToOutputs(result)
	if len(outputs) != 1 || !strings.Contains(outputs[0].Value, "omitted") || len(outputs[0].Value) > maxToolResult {
		t.Fatalf("bounded outputs = %#v", outputs)
	}
	outputs = contentToOutputs(&mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: "image/png"}}})
	if len(outputs) != 1 || !strings.Contains(outputs[0].Value, "unsupported") {
		t.Fatalf("non-text outputs = %#v", outputs)
	}
}

func TestRegistrySubmitsAsyncMCPCallAndTranslatesResult(t *testing.T) {
	host, _, err := NewHost(t.Context(), []ServerConfig{fixtureConfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	base := tool.NewRegistry(tool.StaticTranslators{})
	decorated, issues := DecorateRegistry(base, host)
	if len(issues) != 0 {
		t.Fatalf("decorate issues = %v", issues)
	}
	var echo Tool
	for _, item := range host.Tools() {
		if item.ServerToolName == "echo" {
			echo = item
		}
	}
	translator, ok := decorated.Resolve(echo.Name)
	if !ok {
		t.Fatalf("registry did not expose %q", echo.Name)
	}
	callCtx := &submitContext{}
	status := translator.Translate(callCtx, llm.ToolCall{CallID: "call-1", Name: echo.Name, Arguments: `{"text":"async"}`})
	if status.Error != "" || len(status.WaitingFor) != 1 || callCtx.spec.Type != operation.TypeRemoteJob {
		t.Fatalf("translate status=%#v spec=%#v", status, callCtx.spec)
	}
	ctx, cancel := context.WithCancel(t.Context())
	handler := host.RemoteJobHandlers(ctx)[0]
	defer func() { cancel(); handler.(interface{ Wait() }).Wait() }()
	op := operation.Operation{ID: "mcp-test-1", Type: callCtx.spec.Type, Version: callCtx.spec.Version, Status: operation.StatusReady, State: callCtx.spec.State, MaxOutputLength: callCtx.spec.MaxOutputLength}
	if err := handler.AddRemoteJob(op); err != nil {
		t.Fatal(err)
	}
	completed := waitForCompleted(t, handler.RemoteJobUpdates())
	result, err := translator.TranslateResult("call-1", status, []operation.Operation{completed})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 1 || result.Output[0].Value != "echo:async" {
		t.Fatalf("tool result = %#v", result)
	}
}

func TestAuthenticatedRemoteToolOutputRedactsExactCredentialBeforePersistence(t *testing.T) {
	const secret = "fixture-secret-that-server-echoes"
	server := mcp.NewServer(&mcp.Implementation{Name: "echo-auth", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo_auth", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		middle := len(secret) / 2
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "Authorization: Bearer " + secret[:middle]},
			&mcp.TextContent{Text: secret[middle:]},
		}, IsError: true}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("authorization header=%q", got)
		}
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	store := ConfigStore{Home: t.TempDir()}
	if err := store.AddWithSecret(ServerConfig{ID: "echo", URL: httpServer.URL + "/mcp"}, "bearer", secret); err != nil {
		t.Fatal(err)
	}
	configs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	host, report, err := NewHost(t.Context(), configs)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || len(host.Tools()) != 1 {
		t.Fatalf("report=%+v tools=%+v", report, host.Tools())
	}
	toolItem := host.Tools()[0]
	decorated, issues := DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), host)
	if len(issues) != 0 {
		t.Fatalf("decorate issues=%v", issues)
	}
	translator, ok := decorated.Resolve(toolItem.Name)
	if !ok {
		t.Fatalf("missing translator %q", toolItem.Name)
	}
	callCtx := &submitContext{}
	status := translator.Translate(callCtx, llm.ToolCall{CallID: "call-auth", Name: toolItem.Name, Arguments: `{}`})
	if status.Error != "" || len(status.WaitingFor) != 1 {
		t.Fatalf("translate status=%+v", status)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	remoteHandler := host.RemoteJobHandlers(ctx)[0]
	op := operation.Operation{ID: "op-auth-redact", Type: callCtx.spec.Type, Version: callCtx.spec.Version, Status: operation.StatusReady, State: callCtx.spec.State, MaxOutputLength: callCtx.spec.MaxOutputLength}
	if err := remoteHandler.AddRemoteJob(op); err != nil {
		t.Fatal(err)
	}
	completed := waitForCompleted(t, remoteHandler.RemoteJobUpdates())
	encoded, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("operation persisted echoed credential: %s", encoded)
	}
	result, err := translator.TranslateResult("call-auth", status, []operation.Operation{completed})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 1 || result.Output[0].Value != "MCP tool reported an error: Authorization: Bearer [redacted]" {
		t.Fatalf("model-visible output=%+v", result.Output)
	}
	cancel()
	remoteHandler.(interface{ Wait() }).Wait()
}

func waitForCompleted(t *testing.T, updates <-chan operation.Operation) operation.Operation {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case op, ok := <-updates:
			if !ok {
				t.Fatal("MCP operation updates closed before completion")
			}
			if op.Status == operation.StatusCompleted {
				return op
			}
		case <-deadline:
			t.Fatal("timed out waiting for MCP operation completion")
		}
	}
}

type submitContext struct{ spec operation.Spec }

func (s *submitContext) Submit(spec operation.Spec) operation.ID { s.spec = spec; return "mcp-test-1" }
