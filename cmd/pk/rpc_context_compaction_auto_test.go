package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestRPCForegroundTurnEmitsAutomaticCompactionProgress(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "seed-one", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "First facts recorded."}}}}},
		{response: llm.Response{ID: "seed-two", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Second facts recorded."}}}}},
		{response: llm.Response{ID: "summary", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Earlier facts were recorded in two turns."}}}}},
		{response: llm.Response{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "The current request is complete."}}}}},
	}}
	first, err := runner.Run(context.Background(), runner.Options{Prompt: strings.Repeat("first prior fact ", 2_000), Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low", Adapter: model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), runner.Options{Prompt: strings.Repeat("second prior fact ", 2_000), Workspace: workspace, SessionDir: sessions, SessionID: first.SessionID, Model: "gpt-6-luna", Effort: "low", Adapter: model}); err != nil {
		t.Fatal(err)
	}

	sink := &rpcEventSink{events: make(chan []byte, 64)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard, sessionDir: sessions,
		started: true, session: first.SessionID, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fixture"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)},
		opts: runner.Options{
			Workspace: workspace, SessionDir: sessions, SessionID: first.SessionID, Model: "gpt-6-luna", Effort: "low",
			ContextBudget:     contextbudget.Budget{OperationalInputBudgetTokens: 29_000, OperationalInputSource: contextbudget.SourceOperational},
			HistoryCompaction: runner.HistoryCompactionOptions{Enabled: true, TriggerRatio: .8, TargetRatio: .65, SummaryReserveTokens: 1_000, SummaryInputTokens: 20_000, MaxSummaryTokens: 500, MaxSummaryCalls: 6},
		},
		requestTypes: make(map[string]string),
	}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "auto-compact-turn", Type: "prompt", Payload: json.RawMessage(`{"text":"continue the investigation"}`)}, finished)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("foreground turn failed: %v", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("foreground turn did not finish")
	}
	seen := map[string]bool{}
	var usageRecords []map[string]any
	for len(sink.events) > 0 {
		var event struct {
			ID      string         `json:"id"`
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(<-sink.events, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "context_usage" {
			if event.ID != "auto-compact-turn" || event.Payload["turn_id"] != "auto-compact-turn" {
				t.Fatalf("context usage event was not correlated to active turn: %+v", event)
			}
			if record, ok := event.Payload["context_usage"].(map[string]any); ok {
				usageRecords = append(usageRecords, record)
			}
			continue
		}
		if event.Type != "context_compaction" {
			continue
		}
		if event.ID != "auto-compact-turn" || event.Payload["turn_id"] != "auto-compact-turn" {
			t.Fatalf("automatic compaction event was not correlated to active turn: %+v", event)
		}
		inner, ok := event.Payload["context_compaction"].(map[string]any)
		if !ok || inner["phase"] == nil {
			t.Fatalf("automatic compaction event missing metadata: %+v", event)
		}
		seen[inner["phase"].(string)] = true
	}
	if !seen["started"] || !seen["summarizing"] || !seen["checkpointed"] {
		t.Fatalf("automatic compaction lifecycle not streamed: phases=%v", seen)
	}
	if len(usageRecords) != 2 || usageRecords[0]["pending"] != true || usageRecords[1]["pending"] != false {
		t.Fatalf("expected pending and completed request usage events: %+v", usageRecords)
	}
	if usageRecords[0]["estimated_input_tokens"] == nil || usageRecords[0]["estimate_method"] == nil || usageRecords[0]["compaction_trigger_tokens"] == nil || usageRecords[0]["operational_input_budget_tokens"] != float64(29_000) {
		t.Fatalf("context usage event lacks labeled estimate/budget telemetry: %+v", usageRecords[0])
	}
	if usageRecords[0]["context_limit_tokens"] != nil {
		t.Fatalf("unknown model capacity must remain null: %+v", usageRecords[0])
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = server.adapter.Close()
}
