package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/config"
)

func TestRPCThemeSetPersistsOnlyThemeAndRejectsInvalidValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	cfg := config.Defaults()
	cfg.Model, cfg.Effort = "fixture-model", "high"
	cfg.ContextPolicy = config.ContextPolicyCompact
	cfg.ImageGenDriver = "fixture-image-driver"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: cfgPath, sessionDir: filepath.Join(home, "sessions"), requestTypes: map[string]string{}}
	workspace := t.TempDir()
	start, _ := json.Marshal(map[string]string{"workspace": workspace})
	server.handle(rpcMessage{Version: 1, ID: "start", Type: "start", Payload: start}, make(chan turnDone, 1))
	ready := readRPCEvent(t, sink)
	if ready.Type != "ready" || ready.Payload.(map[string]any)["theme"] != config.DefaultTheme {
		t.Fatalf("start ready theme=%+v", ready)
	}
	var capabilities []string
	for _, value := range ready.Payload.(map[string]any)["capabilities"].([]any) {
		capabilities = append(capabilities, value.(string))
	}
	if !containsString(capabilities, "theme_set") {
		t.Fatalf("theme_set capability missing: %v", capabilities)
	}
	invalid, _ := json.Marshal(map[string]string{"theme": "neon"})
	server.handle(rpcMessage{Version: 1, ID: "bad", Type: "theme_set", Payload: invalid}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" {
		t.Fatalf("invalid theme result=%+v", event)
	}
	persisted, err := config.Load(cfgPath)
	if err != nil || persisted.Theme != config.DefaultTheme {
		t.Fatalf("invalid theme changed persistence: %+v err=%v", persisted, err)
	}
	valid, _ := json.Marshal(map[string]string{"theme": config.ThemeHighContrast})
	server.handle(rpcMessage{Version: 1, ID: "theme", Type: "theme_set", Payload: valid}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "theme_set" || event.Payload.(map[string]any)["theme"] != config.ThemeHighContrast {
		t.Fatalf("theme acknowledgement=%+v", event)
	}
	persisted, err = config.Load(cfgPath)
	if err != nil || persisted.Theme != config.ThemeHighContrast || persisted.Model != "fixture-model" || persisted.Effort != "high" || persisted.ContextPolicy != config.ContextPolicyCompact || persisted.ImageGenDriver != "fixture-image-driver" {
		t.Fatalf("theme update changed unrelated config: %+v err=%v", persisted, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
