package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRPCWebCredentialRoutesAreSanitizedAndIdleOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	t.Setenv("TINYFISH_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)

	server.handle(rpcMessage{Version: 1, ID: "status", Type: "web_status"}, finished)
	status := readRPCEvent(t, sink)
	if status.Type != "web_status" || status.Payload.(map[string]any)["configured"] != false {
		t.Fatalf("initial status=%+v", status)
	}

	secret := "tinyfish-secret-never-render-this"
	payload, _ := json.Marshal(map[string]string{"secret": secret})
	server.handle(rpcMessage{Version: 1, ID: "configure", Type: "web_configure", Payload: payload}, finished)
	configured := readRPCEvent(t, sink)
	if configured.Type != "web_status" {
		t.Fatalf("configure event=%+v", configured)
	}
	encoded, _ := json.Marshal(configured)
	if string(encoded) == "" || bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("credential leaked in event: %s", encoded)
	}
	configuredPayload := configured.Payload.(map[string]any)
	if configuredPayload["configured"] != true || configuredPayload["source"] != "local_store" || configuredPayload["tools_available"] != true {
		t.Fatalf("configured status=%+v", configuredPayload)
	}
	info, err := os.Stat(filepath.Join(home, "websearch.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode info=%v err=%v", info, err)
	}

	server.mu.Lock()
	server.active = true
	server.mu.Unlock()
	server.handle(rpcMessage{Version: 1, ID: "blocked", Type: "web_clear"}, finished)
	blocked := readRPCEvent(t, sink)
	if blocked.Type != "error" {
		t.Fatalf("active clear event=%+v", blocked)
	}
	server.mu.Lock()
	server.active = false
	server.mu.Unlock()

	t.Setenv("TINYFISH_API_KEY", "environment-key")
	server.handle(rpcMessage{Version: 1, ID: "clear", Type: "web_clear"}, finished)
	cleared := readRPCEvent(t, sink)
	if cleared.Type != "web_status" {
		t.Fatalf("clear event=%+v", cleared)
	}
	clearPayload := cleared.Payload.(map[string]any)
	if clearPayload["configured"] != true || clearPayload["source"] != "environment" || clearPayload["environment_override"] != true {
		t.Fatalf("clear status=%+v", clearPayload)
	}
	if _, err := os.Stat(filepath.Join(home, "websearch.json")); !os.IsNotExist(err) {
		t.Fatalf("credential file remains after clear: %v", err)
	}
}
