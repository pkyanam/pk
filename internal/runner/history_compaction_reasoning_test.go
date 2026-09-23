package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestHistorySummaryOmitsOpaqueReasoningPayloads(t *testing.T) {
	request := reviewMultiToolRequest()
	request.Model.ReasoningEffort = llm.ReasoningEffortHigh
	request.Input = append(request.Input[:3], append([]llm.Item{
		{Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: json.RawMessage(`{"opaque":"` + strings.Repeat("SECRET_REASONING", 4096) + `"}`)}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "analysis", Text: "PRIVATE_ANALYSIS_SENTINEL"}},
	}, request.Input[3:]...)...)
	adapter := reviewSummaryAdapter("Preserve the user's task and completed tool outcomes.")
	_, result, err := CompactRequest(context.Background(), request, reviewCompactionOptions(), reviewCompactionBudget(20_000), "reasoning-summary-filter", adapter, &reviewCheckpointStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || len(adapter.requests) == 0 {
		t.Fatalf("expected a summary request: result=%+v calls=%d", result, len(adapter.requests))
	}
	for index, summaryRequest := range adapter.requests {
		if summaryRequest.Model.ReasoningEffort != llm.ReasoningEffortLow {
			t.Fatalf("summary request %d inherited high main-run effort: %q", index, summaryRequest.Model.ReasoningEffort)
		}
		data, err := json.Marshal(summaryRequest)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "SECRET_REASONING") || strings.Contains(string(data), "PRIVATE_ANALYSIS_SENTINEL") {
			t.Fatalf("summary request %d included provider-private reasoning", index)
		}
		if !strings.Contains(string(data), "original task: preserve contract X") {
			t.Fatalf("summary request %d lost ordinary user history", index)
		}
	}
}

func TestDefaultCompactionCallBudgetHandlesMillionTokenWindow(t *testing.T) {
	request := llm.Request{Model: llm.Model{ID: "million-window"}, Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "Keep the task constraints."}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "Complete the ongoing task and preserve this request."}},
	}}
	for index := 0; index < 84; index++ {
		request.Input = append(request.Input, llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("completed work detail ", 1_300)}})
	}
	before := contextbudget.EstimateRequest(request)
	if before.Tokens == nil {
		t.Fatal("fixture should have an available estimate")
	}
	adapter := reviewSummaryAdapter("The ongoing task and its constraints are preserved; earlier work was completed.")
	_, result, err := CompactRequest(context.Background(), request, DefaultHistoryCompactionOptions(), contextbudget.Budget{OperationalInputBudgetTokens: 1_050_000}, "million-window", adapter, &reviewCheckpointStore{}, nil)
	if err != nil {
		t.Fatalf("default policy could not compact large-window history: estimate=%d err=%v", *before.Tokens, err)
	}
	if !result.Compacted || result.SummaryCalls == 0 || result.SummaryCalls > DefaultHistoryCompactionOptions().MaxSummaryCalls {
		t.Fatalf("default policy did not complete within its call bound: %+v", result)
	}
	if result.After.Tokens == nil || *result.After.Tokens > 1_050_000-DefaultHistoryCompactionOptions().SummaryReserveTokens {
		t.Fatalf("compacted projection remains above usable input budget: %+v", result.After)
	}
}

func TestDefaultSummaryChunkAccountsForPriorCheckpoint(t *testing.T) {
	items := []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("old detail ", 7_000)}}}
	request := llm.Request{Model: llm.Model{ID: "million-window"}, Input: items}
	adapter := reviewSummaryAdapter("merged checkpoint")
	engine := historyCompactionAdapter{summarizer: adapter, budget: contextbudget.Budget{OperationalInputBudgetTokens: 1_050_000}}
	policy := DefaultHistoryCompactionOptions()
	_, _, _, err := engine.summarizeRange(context.Background(), request, 0, 1, strings.Repeat("prior summary ", 500), policy)
	if err != nil {
		t.Fatalf("bounded prior summary should fit its first chunk: %v", err)
	}
	for i, summaryRequest := range adapter.requests {
		estimate := contextbudget.EstimateRequest(summaryRequest)
		if estimate.Tokens == nil || *estimate.Tokens > 40_000 {
			t.Fatalf("summary request %d exceeded adaptive allowance: %+v", i, estimate)
		}
	}
}
