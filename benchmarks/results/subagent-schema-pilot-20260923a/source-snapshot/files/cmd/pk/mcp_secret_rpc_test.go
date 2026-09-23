package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/mcpclient"
)

func TestRPCMCPAddStoresCredentialPrivatelyWithoutEchoing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	const secret = "fixture-private-token-value"
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: os.Stderr, requestTypes: map[string]string{}}
	request := `{"server":{"id":"remote","url":"https://mcp.example.test/sse"},"credential_kind":"bearer","secret":"` + secret + `"}`
	server.handle(rpcMessage{Version: 1, ID: "add-secret", Type: "mcp_add", Payload: json.RawMessage(request)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "mcp_updated" {
		t.Fatalf("MCP add response=%+v", event)
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("MCP update event exposed credential: %s", encoded)
	}
	config, err := os.ReadFile(filepath.Join(home, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), secret) {
		t.Fatalf("MCP configuration exposed credential: %s", config)
	}
	secretStore, err := os.ReadFile(filepath.Join(home, "mcp-secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secretStore), secret) {
		t.Fatal("MCP credential was not written to the separate secret store")
	}
	info, err := os.Stat(filepath.Join(home, "mcp-secrets.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret-store mode=%v err=%v", info, err)
	}
	servers, err := (mcpclient.ConfigStore{Home: home}).Summaries()
	if err != nil || len(servers) != 1 || servers[0].AuthMode != "bearer_secret" {
		t.Fatalf("sanitized server summary=%+v err=%v", servers, err)
	}
}

func TestRPCMCPAddCredentialFailureDoesNotEchoSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	const secret = "secret-that-must-not-be-echoed"
	sink := &rpcEventSink{events: make(chan []byte, 2)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: os.Stderr, requestTypes: map[string]string{}}
	request := `{"server":{"id":"remote","url":"https://mcp.example.test/sse"},"credential_kind":"not-supported","secret":"` + secret + `"}`
	server.handle(rpcMessage{Version: 1, ID: "bad-add", Type: "mcp_add", Payload: json.RawMessage(request)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "error" {
		t.Fatalf("invalid credential response=%+v", event)
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("error event exposed credential: %s", encoded)
	}
	if _, err := os.Stat(filepath.Join(home, "mcp-secrets.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid credential created secret store, err=%v", err)
	}
}
