package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/providers"
)

func TestRPCImageGenToolIsAvailableWithExternalCodingProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	workspace, sessions := t.TempDir(), t.TempDir()
	if err := (providers.Store{Home: home}).Put(providers.Provider{
		ID: "fixture", Protocol: providers.ProtocolChatCompletions,
		BaseURL: "https://provider.example.test/v1", APIKey: "fixture-only-secret",
		DefaultModel: "fixture-model", DefaultEffort: "low",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.ImageGenDriver = config.DefaultImageGenDriver
	cfgPath := filepath.Join(home, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: cfgPath, sessionDir: sessions, requestTypes: make(map[string]string),
	}
	server.handle(rpcMessage{
		Version: 1, ID: "start-external", Type: "start",
		Payload: json.RawMessage(`{"workspace":` + mustImageGenJSON(t, workspace) + `,"provider_id":"fixture","model":"fixture-model","effort":"low"}`),
	}, make(chan turnDone, 1))
	ready := readRPCEvent(t, sink)
	if ready.Type != "ready" || ready.Payload.(map[string]any)["provider_id"] != "fixture" {
		t.Fatalf("RPC did not start with the external coding provider: %+v", ready)
	}

	server.handle(rpcMessage{Version: 1, ID: "tools", Type: "tools"}, make(chan turnDone, 1))
	catalog := readRPCEvent(t, sink)
	if catalog.Type != "tool_catalog" {
		t.Fatalf("tools response = %+v", catalog)
	}
	encoded, err := json.Marshal(catalog.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixture-only-secret") {
		t.Fatalf("tool catalog exposed provider credential: %s", encoded)
	}
	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Preview     bool `json:"preview"`
		Initialized bool `json:"initialized"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode tool catalog: %v; payload=%s", err, encoded)
	}
	if !payload.Preview || payload.Initialized {
		t.Fatalf("fresh-session catalog flags: preview=%t initialized=%t", payload.Preview, payload.Initialized)
	}
	for _, tool := range payload.Tools {
		if tool.Name == "ImageGen" {
			return
		}
	}
	t.Fatalf("ImageGen is missing from the external-provider tool catalog: %s", encoded)
}

func mustImageGenJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
