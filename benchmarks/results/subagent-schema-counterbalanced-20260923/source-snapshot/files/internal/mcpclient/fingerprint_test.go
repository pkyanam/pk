package mcpclient

import (
	"strings"
	"testing"
)

func TestSchemaFingerprintChangesWithToolAndSecretConfigWithoutLeakingIt(t *testing.T) {
	makeHost := func(secret, description string) *Host {
		return &Host{
			servers: map[string]*server{"local": {config: ServerConfig{ID: "local", Command: "/opt/mcp/server", Env: map[string]string{"TOKEN": secret}}}},
			ordered: []Tool{{ServerID: "local", ServerToolName: "search", Name: "mcp_local_search_12345678", Description: description, InputSchema: map[string]any{"type": "object"}}},
		}
	}
	one := makeHost("secret-one", "search files")
	fingerprint := one.SchemaFingerprint()
	if fingerprint == "" || strings.Contains(fingerprint, "secret-one") {
		t.Fatalf("invalid or secret-leaking fingerprint %q", fingerprint)
	}
	if got := makeHost("secret-one", "changed description").SchemaFingerprint(); got == fingerprint {
		t.Fatal("tool schema/description change did not change fingerprint")
	}
	if got := makeHost("secret-two", "search files").SchemaFingerprint(); got == fingerprint {
		t.Fatal("environment value change did not change fingerprint")
	}
}
