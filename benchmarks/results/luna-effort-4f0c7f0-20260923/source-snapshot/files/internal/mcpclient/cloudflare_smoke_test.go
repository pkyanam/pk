package mcpclient

import (
	"os"
	"testing"
)

// Enable explicitly for the bounded anonymous upstream-compatibility smoke:
// PK_MCP_CLOUDFLARE_SMOKE=1 go test ./internal/mcpclient -run TestCloudflareDocsAnonymousSmoke -v
func TestCloudflareDocsAnonymousSmoke(t *testing.T) {
	if os.Getenv("PK_MCP_CLOUDFLARE_SMOKE") != "1" {
		t.Skip("set PK_MCP_CLOUDFLARE_SMOKE=1 to contact the public Cloudflare docs MCP endpoint")
	}
	configs := []ServerConfig{{ID: "cloudflare_docs", URL: "https://docs.mcp.cloudflare.com/mcp"}}
	host, report, err := NewHost(t.Context(), configs)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || len(host.Tools()) == 0 {
		t.Fatalf("Cloudflare docs MCP did not load tools: report=%#v tools=%#v", report, host.Tools())
	}
	for _, tool := range host.Tools() {
		t.Logf("tool=%s schema=%v", tool.ServerToolName, tool.InputSchema)
		if tool.ServerToolName == "search_cloudflare_documentation" {
			result, err := host.call(t.Context(), tool.Name, map[string]any{"query": "Cloudflare Workers getting started"})
			if err != nil {
				t.Fatalf("public documentation search call failed: %v", err)
			}
			if result.IsError || len(result.Content) == 0 {
				t.Fatalf("public documentation search returned no result: %#v", result)
			}
			t.Logf("read-only documentation tool returned %d content block(s)", len(result.Content))
			return
		}
	}
	t.Fatal("public Cloudflare documentation search tool was not discovered")
}
