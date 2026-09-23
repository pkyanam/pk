package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type overflowSequenceAdapter struct {
	responses []llm.Response
	errs      []error
	calls     int
}

func (adapter *overflowSequenceAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	index := adapter.calls
	adapter.calls++
	if index >= len(adapter.responses) {
		return llm.Response{}, errors.New("unexpected provider retry")
	}
	return adapter.responses[index], adapter.errs[index]
}

func TestDisabledAutomaticCompactionDoesNotRetryOverflowButManualCompactionWorks(t *testing.T) {
	request := reviewMultiToolRequest()
	overflow := llm.Response{Failure: &llm.Failure{Code: "context_length_exceeded"}}
	main := &overflowSequenceAdapter{responses: []llm.Response{overflow}, errs: []error{errors.New("context length exceeded")}}
	summary := reviewSummaryAdapter("Earlier task constraints and tool outcomes are preserved.")
	engine := historyCompactionAdapter{
		next: main, summarizer: summary, store: &reviewCheckpointStore{}, sessionID: "disabled-overflow",
		budget: reviewCompactionBudget(20_000), policy: HistoryCompactionOptions{Enabled: false},
	}
	_, err := engine.Respond(context.Background(), request, llm.RequestOptions{})
	if err == nil || main.calls != 1 || len(summary.requests) != 0 {
		t.Fatalf("disabled auto-compaction retried/summarized: calls=%d summary_calls=%d err=%v", main.calls, len(summary.requests), err)
	}
	projected, result, err := CompactRequest(context.Background(), request, HistoryCompactionOptions{Enabled: false}, reviewCompactionBudget(20_000), "manual-disabled-auto", reviewSummaryAdapter("Manual factual checkpoint."), &reviewCheckpointStore{}, nil)
	if err != nil || !result.Compacted || len(projected.Input) >= len(request.Input) {
		t.Fatalf("explicit manual compaction should work when automatic mode is disabled: result=%+v err=%v", result, err)
	}
}

func TestContextOverflowRetriesOnceOnlyBeforeAnyOutput(t *testing.T) {
	request := reviewMultiToolRequest()
	main := &overflowSequenceAdapter{
		responses: []llm.Response{
			{Failure: &llm.Failure{Code: "context_length_exceeded"}},
			{Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "completed after one compaction retry"}}}},
		},
		errs: []error{errors.New("context length exceeded"), nil},
	}
	summary := reviewSummaryAdapter("Earlier task constraints and tool outcomes are preserved.")
	engine := historyCompactionAdapter{
		next: main, summarizer: summary, store: &reviewCheckpointStore{}, sessionID: "overflow-retry",
		budget: reviewCompactionBudget(20_000), policy: reviewCompactionOptions(),
	}
	response, err := engine.Respond(context.Background(), request, llm.RequestOptions{})
	if err != nil || main.calls != 2 || len(summary.requests) == 0 || response.Stop != llm.StopComplete {
		t.Fatalf("pre-output overflow should retry once with a checkpoint: calls=%d summaries=%d response=%+v err=%v", main.calls, len(summary.requests), response, err)
	}

	partial := &overflowSequenceAdapter{
		responses: []llm.Response{{Failure: &llm.Failure{Code: "context_length_exceeded"}, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "partial visible output"}}}}},
		errs:      []error{errors.New("context length exceeded")},
	}
	noRetry := historyCompactionAdapter{next: partial, summarizer: summary, store: &reviewCheckpointStore{}, sessionID: "partial-overflow", budget: reviewCompactionBudget(20_000), policy: reviewCompactionOptions()}
	response, err = noRetry.Respond(context.Background(), request, llm.RequestOptions{})
	if err == nil || partial.calls != 1 || len(response.Output) == 0 {
		t.Fatalf("partial output must be returned without replay: calls=%d response=%+v err=%v", partial.calls, response, err)
	}
}

func TestSummaryCallBoundPreflightsFinalMerge(t *testing.T) {
	request := llm.Request{Model: llm.Model{ID: "bounded-summary"}, Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: strings.Repeat("A", 20_000)}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("B", 20_000)}},
	}}
	adapter := reviewSummaryAdapter("summary")
	engine := historyCompactionAdapter{summarizer: adapter, sessionID: "bounded-summary"}
	policy := HistoryCompactionOptions{SummaryInputTokens: 12_000, MaxSummaryTokens: 2_500, MaxSummaryCalls: 2}
	_, _, calls, err := engine.summarizeRange(context.Background(), request, 0, len(request.Input), "", policy)
	if err == nil || calls != 0 || len(adapter.requests) != 0 {
		t.Fatalf("call limit must account for merge before spending provider calls: calls=%d requests=%d err=%v", calls, len(adapter.requests), err)
	}
}
