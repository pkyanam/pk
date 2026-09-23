package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/runner"
)

func TestRPCContextBudgetStatusAndConfigurePersistAndApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	workspace := t.TempDir()
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), started: true, opts: runner.Options{Workspace: workspace, Model: "gpt-6-luna", Effort: "low"}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "status", Type: "context_budget_status"}, make(chan turnDone, 1))
	status := readRPCEvent(t, sink)
	if status.Type != "context_budget" || status.ID != "status" {
		t.Fatalf("status event=%+v", status)
	}
	statusPayload, ok := status.Payload.(map[string]any)
	if !ok {
		t.Fatalf("status payload type %T", status.Payload)
	}
	if statusPayload["provider_id"] != "native" || statusPayload["model_id"] != "gpt-6-luna" || statusPayload["operational_input_budget_tokens"] == nil {
		t.Fatalf("budget status missing resolved metadata: %#v", statusPayload)
	}
	server.handle(rpcMessage{Version: 1, ID: "configure", Type: "context_budget_configure", Payload: json.RawMessage(`{"unknown_input_budget_tokens":96000,"output_reserve_tokens":0,"history_compaction":{"enabled":false,"trigger_ratio":0.9,"target_ratio":0.7},"override":{"provider_id":"native","model_id":"gpt-6-luna","context_tokens":1000000,"input_tokens":900000}}`)}, make(chan turnDone, 1))
	configured := readRPCEvent(t, sink)
	if configured.Type != "context_budget_configured" || configured.ID != "configure" {
		t.Fatalf("configure event=%+v", configured)
	}
	if server.opts.ContextBudget.ProviderID != "native" || server.opts.ContextBudget.ModelID != "gpt-6-luna" || server.opts.ContextBudget.OperationalInputBudgetTokens != 895904 || server.opts.HistoryCompaction.Enabled {
		t.Fatalf("settings not applied to idle session: budget=%+v compaction=%+v", server.opts.ContextBudget, server.opts.HistoryCompaction)
	}
	cfg, err := config.Load(server.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if *cfg.ContextBudget.UnknownInputBudgetTokens != 96000 || *cfg.ContextBudget.OutputReserveTokens != 0 || len(cfg.ContextBudget.Overrides) != 1 || *cfg.ContextBudget.Overrides[0].ContextTokens != 1000000 || *cfg.HistoryCompaction.Enabled {
		t.Fatalf("persisted settings mismatch: %+v", cfg)
	}
}

func TestRPCContextBudgetConfigureRejectsActiveSessionWithoutPersisting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	server := &rpcServer{ctx: context.Background(), output: &rpcEventSink{events: make(chan []byte, 1)}, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), started: true, active: true, opts: runner.Options{Model: "gpt-6-luna"}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "configure", Type: "context_budget_configure", Payload: json.RawMessage(`{"unknown_input_budget_tokens":96000}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, server.output.(*rpcEventSink))
	if event.Type != "error" || event.ID != "configure" {
		t.Fatalf("unexpected active config event: %+v", event)
	}
	cfg, err := config.Load(server.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if *cfg.ContextBudget.UnknownInputBudgetTokens != config.DefaultUnknownInputBudgetTokens {
		t.Fatalf("active config request was persisted: %d", *cfg.ContextBudget.UnknownInputBudgetTokens)
	}
}

func TestRPCNativeProviderSelectionAndSetModelReResolveBudget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), started: true, opts: runner.Options{Model: "gpt-6-luna", Effort: "medium"}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "native", Type: "provider_select", Payload: json.RawMessage(`{"provider_id":"native","model":"custom-model"}`)}, make(chan turnDone, 1))
	selected := readRPCEvent(t, sink)
	if selected.Type != "provider_selected" || server.providerID != "" || server.opts.ContextBudget.ProviderID != "native" || server.opts.ContextBudget.ModelID != "custom-model" {
		t.Fatalf("native selection was not resolved: event=%+v provider=%q budget=%+v", selected, server.providerID, server.opts.ContextBudget)
	}
	server.handle(rpcMessage{Version: 1, ID: "set", Type: "set_model", Payload: json.RawMessage(`{"model":"another-model","effort":"low"}`)}, make(chan turnDone, 1))
	updated := readRPCEvent(t, sink)
	if updated.Type != "status" || server.opts.Model != "another-model" || server.opts.ContextBudget.ModelID != "another-model" || server.opts.ContextBudget.ProviderID != "native" {
		t.Fatalf("set_model did not re-resolve budget: event=%+v opts=%+v", updated, server.opts)
	}
}
