package runner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type reviewCheckpointStore struct{ checkpoint *HistoryCheckpoint }

func (store *reviewCheckpointStore) LoadHistoryCheckpoint(ctx context.Context, _ string) (HistoryCheckpoint, bool, error) {
	if err := ctx.Err(); err != nil {
		return HistoryCheckpoint{}, false, err
	}
	if store.checkpoint == nil {
		return HistoryCheckpoint{}, false, nil
	}
	return *store.checkpoint, true, nil
}

func (store *reviewCheckpointStore) SaveHistoryCheckpoint(ctx context.Context, _ string, checkpoint HistoryCheckpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	copy := checkpoint
	store.checkpoint = &copy
	return nil
}

type reviewCompactionAdapter struct {
	requests []llm.Request
	reply    func(context.Context, llm.Request) (llm.Response, error)
}

func (adapter *reviewCompactionAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	adapter.requests = append(adapter.requests, request)
	return adapter.reply(ctx, request)
}

func reviewSummaryAdapter(text string) *reviewCompactionAdapter {
	return &reviewCompactionAdapter{reply: func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Stop: llm.StopComplete, Usage: llm.Usage{InputTokens: 21, OutputTokens: 7}, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: text}}}}, nil
	}}
}

func reviewCompactionBudget(input int64) contextbudget.Budget {
	return contextbudget.Budget{
		OperationalInputBudgetTokens: input,
		OperationalInputSource:       contextbudget.SourceOperational,
	}
}

func reviewCompactionOptions() HistoryCompactionOptions {
	return HistoryCompactionOptions{
		Enabled: true, TriggerRatio: .8, TargetRatio: .65,
		SummaryReserveTokens: 500, SummaryInputTokens: 10_000,
		MaxSummaryTokens: 500, MaxSummaryCalls: 4,
	}
}

func reviewMultiToolRequest() llm.Request {
	return llm.Request{Model: llm.Model{ID: "test-model"}, Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "stable system"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "additional immutable provider policy"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "original task: preserve contract X"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Inspecting files."}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-a", Name: "Read", Arguments: `{"path":"a.go"}`}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-b", Name: "Read", Arguments: `{"path":"b.go"}`}},
		{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call-b", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: strings.Repeat("B", 1_500)}}}},
		{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call-a", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: strings.Repeat("A", 1_500)}}}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Both reads completed."}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "current request: implement and test"}},
	}, Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "Read", Description: "Read files", Parameters: map[string]any{"type": "object"}}}}
}

func TestReviewHistoryCheckpointKeepsCurrentPromptAndClosedToolPairs(t *testing.T) {
	request := reviewMultiToolRequest()
	store := &reviewCheckpointStore{}
	adapter := reviewSummaryAdapter("Preserve contract X. Both source files were read; implement the requested change and run tests.")
	projected, result, err := CompactRequest(context.Background(), request, reviewCompactionOptions(), reviewCompactionBudget(20_000), "review-closed-pairs", adapter, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.CompactedItems == 0 || store.checkpoint == nil {
		t.Fatalf("checkpoint did not advance: result=%+v saved=%v", result, store.checkpoint != nil)
	}
	if got := projected.Input[len(projected.Input)-1].Data.(llm.Message).Text; got != "current request: implement and test" {
		t.Fatalf("current user prompt changed: %q", got)
	}
	for index, want := range []string{"stable system", "additional immutable provider policy"} {
		if index >= len(projected.Input) {
			t.Fatalf("projection dropped system item %q", want)
		}
		message, ok := projected.Input[index].Data.(llm.Message)
		if !ok || message.Role != llm.RoleSystem || message.Text != want {
			t.Fatalf("system prefix item %d changed: got=%#v want=%q", index, projected.Input[index].Data, want)
		}
	}
	if len(projected.Tools) != len(request.Tools) || projected.Tools[0].Name != request.Tools[0].Name {
		t.Fatalf("tool schema was lost during projection: %+v", projected.Tools)
	}
	calls, results := map[string]int{}, map[string]int{}
	for _, item := range projected.Input {
		switch data := item.Data.(type) {
		case llm.ToolCall:
			calls[data.CallID]++
		case llm.ToolResult:
			results[data.CallID]++
		}
	}
	for callID, count := range calls {
		if count != results[callID] {
			t.Fatalf("compaction left an unmatched call/result pair %q: calls=%d results=%d", callID, count, results[callID])
		}
	}
	for callID, count := range results {
		if count != calls[callID] {
			t.Fatalf("compaction left a tool result without its call %q: calls=%d results=%d", callID, calls[callID], count)
		}
	}
	if len(adapter.requests) == 0 {
		t.Fatal("summary adapter was not called")
	}
	encodedSummaryRequest, _ := json.Marshal(adapter.requests[0].Input)
	if !strings.Contains(string(encodedSummaryRequest), "untrusted data") {
		t.Fatal("summary instruction did not frame history as untrusted")
	}
}

func TestReviewHistoryCheckpointDoesNotCutAnUnansweredQuestionOrToolCall(t *testing.T) {
	request := llm.Request{Model: llm.Model{ID: "test-model"}, Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "system"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "old task"}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "pending-question", Name: "AskUser", Arguments: `{"question":"continue?"}`}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "new steering that must wait"}},
	}}
	store := &reviewCheckpointStore{}
	adapter := reviewSummaryAdapter("should not be called")
	projected, result, err := CompactRequest(context.Background(), request, reviewCompactionOptions(), reviewCompactionBudget(20_000), "review-pending-question", adapter, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compacted || len(adapter.requests) != 0 || store.checkpoint != nil || !reflect.DeepEqual(projected, request) {
		t.Fatalf("pending question was summarized or checkpointed: result=%+v calls=%d saved=%v", result, len(adapter.requests), store.checkpoint != nil)
	}
}

func TestReviewHistoryCheckpointFailureLeavesPreviousCheckpointUntouched(t *testing.T) {
	request := reviewMultiToolRequest()
	old := HistoryCheckpoint{Version: historyCheckpointVersion, CutCount: 1, PrefixSHA256: fingerprintItems(request.Input[:1]), Summary: "previous complete checkpoint"}
	store := &reviewCheckpointStore{checkpoint: &old}
	adapter := &reviewCompactionAdapter{reply: func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{}, errors.New("summary provider unavailable")
	}}
	_, _, err := CompactRequest(context.Background(), request, reviewCompactionOptions(), reviewCompactionBudget(20_000), "review-summary-failure", adapter, store, nil)
	if err == nil {
		t.Fatal("failed summarization advanced a checkpoint")
	}
	if !reflect.DeepEqual(*store.checkpoint, old) {
		t.Fatalf("previous checkpoint changed after summary failure: got=%+v want=%+v", *store.checkpoint, old)
	}
}

func TestReviewHistoryCheckpointCanceledBeforeSummaryDoesNotSave(t *testing.T) {
	request := reviewMultiToolRequest()
	store := &reviewCheckpointStore{}
	adapter := reviewSummaryAdapter("unused")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := CompactRequest(ctx, request, reviewCompactionOptions(), reviewCompactionBudget(20_000), "review-canceled", adapter, store, nil)
	if err == nil {
		t.Fatal("canceled compaction returned success")
	}
	if store.checkpoint != nil || len(adapter.requests) != 0 {
		t.Fatalf("canceled compaction performed work: saved=%v summary_calls=%d", store.checkpoint != nil, len(adapter.requests))
	}
}

func TestReviewHistoryCheckpointCompactsLongSingleUserToolLoop(t *testing.T) {
	const originalTask = "original long-running task: preserve this exact requirement"
	request := llm.Request{Model: llm.Model{ID: "test-model"}, Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "stable system"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: originalTask}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "I will inspect both modules."}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "old-call", Name: "Read", Arguments: `{"path":"old.go"}`}},
		{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "old-call", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: strings.Repeat("old evidence ", 150)}}}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "I will now verify the change."}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "recent-call", Name: "Read", Arguments: `{"path":"recent.go"}`}},
		{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "recent-call", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: strings.Repeat("recent verification evidence ", 60)}}}},
	}}
	policy := reviewCompactionOptions()
	policy.MaxSummaryTokens = 100
	policy.SummaryReserveTokens = 200
	store := &reviewCheckpointStore{}
	adapter := reviewSummaryAdapter("The old module was inspected. The task requires preserving its exact public contract.")
	projected, result, err := CompactRequest(context.Background(), request, policy, reviewCompactionBudget(5_000), "review-single-user-loop", adapter, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || store.checkpoint == nil {
		t.Fatalf("single-user loop did not compact: result=%+v", result)
	}
	userCopies := 0
	calls, results := map[string]int{}, map[string]int{}
	for _, item := range projected.Input {
		switch data := item.Data.(type) {
		case llm.Message:
			if data.Role == llm.RoleUser && data.Text == originalTask {
				userCopies++
			}
		case llm.ToolCall:
			calls[data.CallID]++
		case llm.ToolResult:
			results[data.CallID]++
		}
	}
	if userCopies != 1 {
		t.Fatalf("original user task was not retained exactly once: count=%d projected=%#v", userCopies, projected.Input)
	}
	if calls["recent-call"] != 1 || results["recent-call"] != 1 {
		t.Fatalf("recent complete tool result was lost or unpaired: calls=%v results=%v", calls, results)
	}
	if calls["old-call"] != results["old-call"] {
		t.Fatalf("older tool call/result boundary was split: calls=%v results=%v", calls, results)
	}
}

func TestReviewSummaryPromptAndOutputReserveFitConfiguredSummaryBudget(t *testing.T) {
	const summaryInputBudget int64 = 1_000
	request := llm.Request{Model: llm.Model{ID: "test-model"}, Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "stable system"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "preserve my task"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "checking files"}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call", Name: "Read", Arguments: `{"path":"file.go"}`}},
		{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: strings.Repeat("quoted output \" and newline\n", 95)}}}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "continue with the current request"}},
	}}
	policy := reviewCompactionOptions()
	policy.SummaryInputTokens = summaryInputBudget
	policy.MaxSummaryTokens = 128
	policy.SummaryReserveTokens = 256
	store := &reviewCheckpointStore{}
	adapter := reviewSummaryAdapter("A bounded factual summary.")
	_, _, err := CompactRequest(context.Background(), request, policy, reviewCompactionBudget(8_000), "review-summary-budget", adapter, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) == 0 {
		t.Fatal("expected a summary request")
	}
	for index, summaryRequest := range adapter.requests {
		estimate := contextbudget.EstimateRequest(summaryRequest)
		if estimate.Tokens == nil || *estimate.Tokens > summaryInputBudget {
			t.Fatalf("summary request %d exceeded the input budget after system/JSON framing: estimate=%+v", index, estimate)
		}
	}
	if store.checkpoint == nil {
		t.Fatal("successful bounded summary did not save a checkpoint")
	}
	if got := store.checkpoint.SummaryCalls; got != len(adapter.requests) {
		t.Fatalf("checkpoint summary call accounting = %d, actual calls = %d", got, len(adapter.requests))
	}
	if store.checkpoint.SummaryInputTokens != 21*int64(len(adapter.requests)) || store.checkpoint.SummaryOutputTokens != 7*int64(len(adapter.requests)) {
		t.Fatalf("checkpoint summary usage accounting is wrong: %+v", store.checkpoint)
	}
}

func TestReviewContextOverflowClassifierDoesNotTreatRateLimitsAsCompactionSignals(t *testing.T) {
	cases := []struct {
		name string
		resp llm.Response
		err  error
		want bool
	}{
		{name: "structured context overflow", resp: llm.Response{Failure: &llm.Failure{Code: "context_length_exceeded"}}, want: true},
		{name: "rate limit mentioning token count", resp: llm.Response{Failure: &llm.Failure{Code: "rate_limit_exceeded", Message: "too many tokens per minute"}}},
		{name: "generic transport failure", err: errors.New("upstream connection closed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextOverflow(tc.resp, tc.err); got != tc.want {
				t.Fatalf("isContextOverflow() = %v, want %v", got, tc.want)
			}
		})
	}
}
