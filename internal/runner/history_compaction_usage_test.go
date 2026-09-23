package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type usageSequenceAdapter struct {
	responses []llm.Response
	errors    []error
	calls     int
}

func (adapter *usageSequenceAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	index := adapter.calls
	adapter.calls++
	if index >= len(adapter.responses) {
		return llm.Response{}, errors.New("unexpected summary call")
	}
	return adapter.responses[index], adapter.errors[index]
}

func TestSummaryUsageAdapterRecordsFailedResponseUsage(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalHistoryCompactionUsageStore(dir)
	providerErr := errors.New("private transport detail")
	provider := &usageSequenceAdapter{
		responses: []llm.Response{{Failure: &llm.Failure{Code: "overloaded"}, Usage: llm.Usage{InputTokens: 19, OutputTokens: 4, Raw: json.RawMessage(`{"input_tokens":19,"output_tokens":4}`)}}},
		errors:    []error{providerErr},
	}
	adapter := compactionUsageAdapter{next: provider, store: store, session: "failed-summary"}
	response, err := adapter.Respond(context.Background(), llm.Request{}, llm.RequestOptions{})
	if !errors.Is(err, providerErr) || response.Failure == nil {
		t.Fatalf("summary response was altered: response=%+v err=%v", response, err)
	}
	got, found, err := store.LoadHistoryCompactionUsage(context.Background(), "failed-summary")
	if err != nil || !found {
		t.Fatalf("failed summary usage missing: found=%v err=%v", found, err)
	}
	if got.Attempts != 1 || got.Completed != 0 || got.Failed != 1 || got.InputTokens == nil || *got.InputTokens != 19 || got.OutputTokens == nil || *got.OutputTokens != 4 {
		t.Fatalf("failed summary usage not retained: %+v", got)
	}
}

func TestSummaryUsageAdapterRecordsEachChunkBeforeLaterFailure(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalHistoryCompactionUsageStore(dir)
	provider := &usageSequenceAdapter{
		responses: []llm.Response{
			{Stop: llm.StopComplete, Usage: llm.Usage{InputTokens: 11, OutputTokens: 3, Raw: json.RawMessage(`{"input_tokens":11,"output_tokens":3}`)}, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "first bounded checkpoint"}}}},
			{Failure: &llm.Failure{Code: "overloaded"}, Usage: llm.Usage{InputTokens: 17, OutputTokens: 5, Raw: json.RawMessage(`{"input_tokens":17,"output_tokens":5}`)}},
		},
		errors: []error{nil, errors.New("private second-call failure")},
	}
	tracked := &compactionUsageAdapter{next: provider, store: store, session: "partial-summary"}
	items := make([]llm.Item, 8)
	for i := range items {
		role := llm.RoleUser
		if i%2 == 1 {
			role = llm.RoleAssistant
		}
		items[i] = llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: strings.Repeat("bounded history detail ", 65)}}
	}
	engine := historyCompactionAdapter{summarizer: tracked, sessionID: "partial-summary"}
	policy := HistoryCompactionOptions{SummaryInputTokens: 4_000, MaxSummaryTokens: 500, MaxSummaryCalls: 6}
	_, _, _, err := engine.summarizeRange(context.Background(), llm.Request{Model: llm.Model{ID: "fixture"}, Input: items}, 0, len(items), "", policy)
	if err == nil {
		t.Fatal("expected the second summary chunk to fail")
	}
	if provider.calls != 2 {
		t.Fatalf("expected a successful first chunk and failed second chunk, calls=%d err=%v", provider.calls, err)
	}
	got, found, err := store.LoadHistoryCompactionUsage(context.Background(), "partial-summary")
	if err != nil || !found {
		t.Fatalf("partial summary usage missing: found=%v err=%v", found, err)
	}
	if got.Attempts != 2 || got.Completed != 1 || got.Failed != 1 || got.InputTokens == nil || *got.InputTokens != 28 || got.InputCalls != 2 || got.OutputTokens == nil || *got.OutputTokens != 8 || got.OutputCalls != 2 {
		t.Fatalf("partial multi-chunk usage was not retained: %+v", got)
	}
}

func TestHistoryCompactionUsageRecordsEveryAttemptAndPartialAvailability(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalHistoryCompactionUsageStore(dir)
	full := llm.Response{Stop: llm.StopComplete, Usage: llm.Usage{
		InputTokens: 10, OutputTokens: 5, CachedInputTokens: 2,
		Raw: json.RawMessage(`{"input_tokens":10,"output_tokens":5,"cached_input_tokens":2,"cache_write_input_tokens":0}`),
	}}
	if err := store.RecordHistoryCompactionAttempt(context.Background(), "usage-session", full, nil); err != nil {
		t.Fatal(err)
	}
	failed := llm.Response{Failure: &llm.Failure{Code: "upstream_failed", Message: "private provider detail"}, Usage: llm.Usage{
		InputTokens: 7, OutputTokens: 2, Raw: json.RawMessage(`{"input_tokens":7,"output_tokens":2}`),
	}}
	if err := store.RecordHistoryCompactionAttempt(context.Background(), "usage-session", failed, errors.New("private transport detail")); err != nil {
		t.Fatal(err)
	}
	unknown := llm.Response{Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "private summary content"}}}}
	if err := store.RecordHistoryCompactionAttempt(context.Background(), "usage-session", unknown, errors.New("provider call failed")); err != nil {
		t.Fatal(err)
	}

	got, found, err := LoadHistoryCompactionUsage(context.Background(), dir, "usage-session")
	if err != nil || !found {
		t.Fatalf("load summary usage = (%+v, %v, %v)", got, found, err)
	}
	if got.Attempts != 3 || got.Completed != 1 || got.Failed != 2 || got.UnknownUsageAttempts != 1 {
		t.Fatalf("attempt outcomes = %+v", got)
	}
	if got.InputTokens == nil || *got.InputTokens != 17 || got.InputCalls != 2 || got.OutputTokens == nil || *got.OutputTokens != 7 || got.OutputCalls != 2 {
		t.Fatalf("partial input/output totals = %+v", got)
	}
	if got.CachedInputTokens == nil || *got.CachedInputTokens != 2 || got.CachedInputCalls != 1 || got.CacheWriteInputTokens == nil || *got.CacheWriteInputTokens != 0 || got.CacheWriteInputCalls != 1 {
		t.Fatalf("cache usage/explicit zero lost: %+v", got)
	}
	path := filepath.Join(dir, historyCompactionUsageFilename("usage-session"))
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("usage ledger permissions = %v, err=%v", info.Mode().Perm(), err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private provider detail", "private transport detail", "private summary content", "provider call failed"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private response/error text leaked into numeric sidecar: %q", private)
		}
	}
}

func TestHistoryCompactionUsageMissingAndCanceledCalls(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalHistoryCompactionUsageStore(dir)
	if _, found, err := LoadHistoryCompactionUsage(context.Background(), dir, "legacy"); err != nil || found {
		t.Fatalf("missing legacy ledger = found %v, err %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.RecordHistoryCompactionAttempt(ctx, "session", llm.Response{}, context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ledger write error = %v", err)
	}
	if _, found, err := LoadHistoryCompactionUsage(context.Background(), dir, "session"); err != nil || found {
		t.Fatalf("canceled write persisted a false attempt: found=%v err=%v", found, err)
	}
}

func historyCompactionUsageFilename(sessionID string) string {
	return filepath.Base((&fileHistoryCompactionUsageStore{directory: "/"}).path(sessionID))
}
