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

func TestProviderOutputUsageMayIncludeReasoningAboveSummaryTextCeiling(t *testing.T) {
	request := reviewMultiToolRequest()
	adapter := &reviewCompactionAdapter{reply: func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{
			Stop:  llm.StopComplete,
			Usage: llm.Usage{InputTokens: 4_474, OutputTokens: 3_113},
			Output: []llm.Item{
				{Type: llm.ItemReasoning, Data: llm.Reasoning{Summary: []string{"provider metering includes internal reasoning"}}},
				{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "The requested work and completed tool outcomes are preserved."}},
			},
		}, nil
	}}
	store := &reviewCheckpointStore{}
	_, result, err := CompactRequest(context.Background(), request, reviewCompactionOptions(), reviewCompactionBudget(20_000), "reasoning-output-usage", adapter, store, nil)
	if err != nil {
		t.Fatalf("provider usage including reasoning should not reject bounded summary text: %v", err)
	}
	if !result.Compacted || store.checkpoint == nil {
		t.Fatalf("bounded checkpoint was not saved: result=%+v saved=%v", result, store.checkpoint != nil)
	}
	if !result.SummaryUsage.OutputAvailable || result.SummaryUsage.OutputTokens != 3_113 {
		t.Fatalf("provider output usage was not retained: %+v", result.SummaryUsage)
	}
}

func TestVisibleSummaryTextLimitPreservesPreviousCheckpoint(t *testing.T) {
	request := reviewMultiToolRequest()
	prior := HistoryCheckpoint{
		Version: historyCheckpointVersion, CutCount: 1,
		PrefixSHA256: fingerprintItems(request.Input[:1]), Summary: "previous saved checkpoint",
	}
	store := &reviewCheckpointStore{checkpoint: &prior}
	policy := reviewCompactionOptions()
	policy.MaxSummaryTokens = 100
	adapter := reviewSummaryAdapter(strings.Repeat("oversized-visible-summary ", 30))
	_, _, err := CompactRequest(context.Background(), request, policy, reviewCompactionBudget(20_000), "oversized-summary", adapter, store, nil)
	if err == nil || !strings.Contains(err.Error(), "empty or oversized content") || len(adapter.requests) == 0 {
		t.Fatalf("oversized visible summary should be rejected after provider call: calls=%d err=%v", len(adapter.requests), err)
	}
	if store.checkpoint == nil || store.checkpoint.Summary != prior.Summary || store.checkpoint.CutCount != prior.CutCount {
		t.Fatalf("oversized summary replaced the prior checkpoint: got=%+v want=%+v", store.checkpoint, prior)
	}
}

func TestVisibleMergedSummaryTextLimitIsEnforced(t *testing.T) {
	request := llm.Request{Model: llm.Model{ID: "merge-limit"}, Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: strings.Repeat("A", 20_000)}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("B", 20_000)}},
	}}
	var adapter *reviewCompactionAdapter
	adapter = &reviewCompactionAdapter{reply: func(_ context.Context, _ llm.Request) (llm.Response, error) {
		if len(adapter.requests) < 3 {
			return llm.Response{Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "bounded segment"}}}}, nil
		}
		return llm.Response{Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("too long ", 1_000)}}}}, nil
	}}
	engine := historyCompactionAdapter{summarizer: adapter}
	policy := HistoryCompactionOptions{SummaryInputTokens: 12_000, MaxSummaryTokens: 100, MaxSummaryCalls: 4}
	_, _, _, err := engine.summarizeRange(context.Background(), request, 0, len(request.Input), "", policy)
	if err == nil || len(adapter.requests) != 3 {
		t.Fatalf("oversized merge result should be rejected after two segments: calls=%d err=%v", len(adapter.requests), err)
	}

	var metered *reviewCompactionAdapter
	metered = &reviewCompactionAdapter{reply: func(_ context.Context, _ llm.Request) (llm.Response, error) {
		usage := llm.Usage{}
		text := "bounded segment"
		if len(metered.requests) == 3 {
			usage.OutputTokens = 3_113 // Provider metering includes reasoning tokens.
			text = "merged visible checkpoint"
		}
		return llm.Response{Stop: llm.StopComplete, Usage: usage, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: text}}}}, nil
	}}
	engine.summarizer = metered
	_, usage, _, err := engine.summarizeRange(context.Background(), request, 0, len(request.Input), "", policy)
	if err != nil {
		t.Fatalf("high provider output usage from merge reasoning should not reject bounded visible text: %v", err)
	}
	if !usage.OutputAvailable || usage.OutputTokens != 3_113 {
		t.Fatalf("merge provider usage was not retained: %+v", usage)
	}
}
