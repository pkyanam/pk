package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/config"
)

func TestRPCLoadsContextPolicyAndPreservesItWhenSavingModelDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	if err := config.Save(cfgPath, config.Config{Model: "gpt-6-luna", Effort: "medium", ContextPolicy: config.ContextPolicyCompact}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: cfgPath, sessionDir: filepath.Join(home, "sessions"),
		requestTypes: map[string]string{},
	}
	startPayload, _ := json.Marshal(map[string]string{"workspace": workspace})
	server.handle(rpcMessage{Version: 1, ID: "start", Type: "start", Payload: startPayload}, make(chan turnDone, 1))
	ready := readRPCEvent(t, sink)
	if ready.Type != "ready" {
		t.Fatalf("start event=%+v", ready)
	}
	if !server.opts.CompactCapturedOutput {
		t.Fatal("RPC start did not apply compact context policy")
	}

	setModel, _ := json.Marshal(map[string]string{"model": "gpt-6-luna", "effort": "high"})
	server.handle(rpcMessage{Version: 1, ID: "model", Type: "set_model", Payload: setModel}, make(chan turnDone, 1))
	if status := readRPCEvent(t, sink); status.Type != "status" {
		t.Fatalf("set_model event=%+v", status)
	}
	persisted, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ContextPolicy != config.ContextPolicyCompact || persisted.Effort != "high" {
		t.Fatalf("set_model lost unrelated context policy: %+v", persisted)
	}
}
