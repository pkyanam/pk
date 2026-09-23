package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStreamableHTTPConnectsAndUsesEnvironmentCredential(t *testing.T) {
	t.Setenv("PK_MCP_TEST_HEADER", "private-fixture-secret")
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "hello", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if got := r.Header.Get("X-Test-Key"); got != "private-fixture-secret" {
			t.Errorf("credential header = %q", got)
		}
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	config := ServerConfig{ID: "remote", URL: httpServer.URL + "/mcp", Auth: HTTPAuthConfig{Mode: "header_env", HeaderName: "X-Test-Key", HeaderValueEnv: "PK_MCP_TEST_HEADER"}}
	host, report, err := NewHost(t.Context(), []ServerConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || len(host.Tools()) != 1 {
		t.Fatalf("report=%#v tools=%#v", report, host.Tools())
	}
	result, err := host.call(t.Context(), host.Tools()[0].Name, map[string]any{})
	if err != nil || result.IsError {
		t.Fatalf("call result=%#v err=%v", result, err)
	}
	if strings.Contains(host.SchemaFingerprint(), "private-fixture-secret") {
		t.Fatal("fingerprint exposed credential")
	}
}

func TestStreamableHTTPUsesStoredCredentialWithoutExposingIt(t *testing.T) {
	const secret = "one-shot-fixture-key"
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "hello", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("authorization header = %q", got)
		}
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	store := ConfigStore{Home: t.TempDir()}
	if err := store.AddWithSecret(ServerConfig{ID: "remote", URL: httpServer.URL + "/mcp"}, "bearer", secret); err != nil {
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
		t.Fatalf("report=%#v tools=%#v", report, host.Tools())
	}
	if strings.Contains(host.Tools()[0].Description, secret) || strings.Contains(host.SchemaFingerprint(), secret) {
		t.Fatal("credential leaked through tool metadata/fingerprint")
	}
}

func TestRemoteURLAndAuthValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  ServerConfig
		wantErr bool
	}{
		{"https", ServerConfig{ID: "x", URL: "https://example.com/mcp"}, false},
		{"loopback-http", ServerConfig{ID: "x", URL: "http://127.0.0.1:1234/mcp"}, false},
		{"remote-http", ServerConfig{ID: "x", URL: "http://example.com/mcp"}, true},
		{"userinfo", ServerConfig{ID: "x", URL: "https://user:pass@example.com/mcp"}, true},
		{"query", ServerConfig{ID: "x", URL: "https://example.com/mcp?token=secret"}, true},
		{"mixed", ServerConfig{ID: "x", URL: "https://example.com/mcp", Command: "/bin/true"}, true},
		{"invalid-header", ServerConfig{ID: "x", URL: "https://example.com/mcp", Auth: HTTPAuthConfig{Mode: "header_env", HeaderName: "Bad Header", HeaderValueEnv: "TOKEN"}}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("validate error=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestAuthenticatedRemoteDiagnosticsSuppressServerBodies(t *testing.T) {
	t.Setenv("PK_MCP_ERROR_TOKEN", "secret-never-display")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("server echoed secret-never-display"))
	}))
	defer server.Close()
	_, report, err := NewHost(t.Context(), []ServerConfig{{ID: "private", URL: server.URL + "/mcp", Auth: HTTPAuthConfig{Mode: "bearer_env", BearerEnv: "PK_MCP_ERROR_TOKEN"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 1 || strings.Contains(report.Warnings[0], "secret-never-display") || strings.Contains(report.Warnings[0], "server echoed") {
		t.Fatalf("diagnostic leaked remote auth response: %#v", report.Warnings)
	}
}
