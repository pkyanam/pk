package runner

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/benchcontext"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

type contextUsageFixtureAdapter struct {
	response llm.Response
	started  chan struct{}
	release  chan struct{}
}

type contextUsageLateAdapter struct {
	started  chan struct{}
	release  chan struct{}
	response llm.Response
}

func (adapter contextUsageLateAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	close(adapter.started)
	<-adapter.release // Deliberately model a transport that returns after cancellation.
	return adapter.response, nil
}

func (adapter contextUsageFixtureAdapter) Respond(ctx context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	if adapter.started != nil {
		close(adapter.started)
		select {
		case <-adapter.release:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	return adapter.response, nil
}

func TestContextUsageSidecarPairsPendingRequestAndResponseWithoutContents(t *testing.T) {
	dir := t.TempDir()
	store := fileContextUsageStore{directory: dir}
	started, release := make(chan struct{}), make(chan struct{})
	request := llm.Request{
		Input: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "PRIVATE_SYSTEM"}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "PRIVATE_USER"}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "PRIVATE_CALL", Name: "PRIVATE_TOOL", Arguments: `{"secret":"PRIVATE_ARGUMENT"}`}},
			{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "PRIVATE_CALL", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "PRIVATE_RESULT"}}}},
		},
		Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "PRIVATE_SCHEMA", Description: "PRIVATE_DESCRIPTION", Parameters: map[string]any{"type": "object"}}},
	}
	response := llm.Response{ID: "PRIVATE_RESPONSE_ID", Usage: llm.Usage{
		InputTokens: 10, OutputTokens: 4, CachedInputTokens: 2,
		Raw: json.RawMessage(`{"input_tokens":10,"output_tokens":4,"input_tokens_details":{"cached_tokens":2}}`),
	}}
	adapter := &contextUsageAdapter{next: contextUsageFixtureAdapter{response: response, started: started, release: release}, store: store, id: session.ID("session-context-usage")}
	finished := make(chan error, 1)
	go func() {
		_, err := adapter.Respond(context.Background(), request, llm.RequestOptions{})
		finished <- err
	}()
	<-started
	pending, err := store.LoadContextUsage(context.Background(), adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	if !pending.Available || !pending.Pending || pending.RequestOrdinal != 1 || pending.LatestProviderUsage.InputAvailable || pending.LatestProviderUsage.InputTokens != nil {
		t.Fatalf("pending measurement=%+v", pending)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	complete, err := store.LoadContextUsage(context.Background(), adapter.id)
	if err != nil {
		t.Fatal(err)
	}
	if complete.Pending || complete.RequestOrdinal != 1 || complete.Measurement != "json_value_bytes" {
		t.Fatalf("completed measurement=%+v", complete)
	}
	if !complete.LatestProviderUsage.InputAvailable || complete.LatestProviderUsage.InputTokens == nil || *complete.LatestProviderUsage.InputTokens != 10 || !complete.LatestProviderUsage.CachedAvailable || complete.LatestProviderUsage.CachedInputTokens == nil || *complete.LatestProviderUsage.CachedInputTokens != 2 || !complete.LatestProviderUsage.OutputAvailable || complete.LatestProviderUsage.OutputTokens == nil || *complete.LatestProviderUsage.OutputTokens != 4 {
		t.Fatalf("latest provider usage=%+v", complete.LatestProviderUsage)
	}
	wantIDs := []string{"system_prompt", "tool_schemas", "messages", "tool_calls", "tool_results", "other_input"}
	if len(complete.Categories) != len(wantIDs) {
		t.Fatalf("categories=%+v", complete.Categories)
	}
	var total int64
	for i, category := range complete.Categories {
		if category.ID != wantIDs[i] {
			t.Fatalf("category[%d]=%q, want %q", i, category.ID, wantIDs[i])
		}
		total += category.Bytes
	}
	if total != complete.TotalBytes || complete.TotalBytes <= 0 || complete.ContextLimitTokens != nil {
		t.Fatalf("total/category bytes=%d/%d context limit=%v", complete.TotalBytes, total, complete.ContextLimitTokens)
	}
	data, err := os.ReadFile(store.path(adapter.id))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE_", "PRIVATE_RESPONSE_ID", "PRIVATE_ARGUMENT", "PRIVATE_SCHEMA"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("context usage metadata leaked %q: %s", secret, data)
		}
	}
	info, err := os.Stat(store.path(adapter.id))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata mode=%o, want 600", info.Mode().Perm())
	}
	loaded, available, err := LoadContextUsage(context.Background(), dir, "", string(adapter.id))
	if err != nil || !available || loaded.RequestOrdinal != 1 {
		t.Fatalf("public load=%+v available=%v err=%v", loaded, available, err)
	}
}

func TestContextUsageUnavailableForLegacyAndMonotonicOnResume(t *testing.T) {
	if _, available, err := LoadContextUsage(context.Background(), t.TempDir(), "", "legacy"); err != nil || available {
		t.Fatalf("legacy metadata available=%v err=%v", available, err)
	}
	dir := t.TempDir()
	store := fileContextUsageStore{directory: dir}
	if err := store.SaveContextUsage(context.Background(), "session-resume", ContextUsageRecord{Available: true, RequestOrdinal: 8}); err != nil {
		t.Fatal(err)
	}
	prior, err := store.LoadContextUsage(context.Background(), "session-resume")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &contextUsageAdapter{next: contextUsageFixtureAdapter{response: llm.Response{Usage: llm.Usage{}}}, store: store, id: "session-resume", ordinal: prior.RequestOrdinal}
	_, err = adapter.Respond(context.Background(), llm.Request{Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "next"}}}}, llm.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := store.LoadContextUsage(context.Background(), "session-resume")
	if err != nil || latest.RequestOrdinal != 9 || latest.Pending {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestCanceledLateContextUsageCannotOverwriteNextRequest(t *testing.T) {
	store := fileContextUsageStore{directory: t.TempDir()}
	id := session.ID("session-late-usage")
	started, release := make(chan struct{}), make(chan struct{})
	oldResponse := llm.Response{ID: "old-response", Usage: llm.Usage{InputTokens: 11, OutputTokens: 2, Raw: json.RawMessage(`{"input_tokens":11,"output_tokens":2}`)}}
	oldAdapter := &contextUsageAdapter{next: contextUsageLateAdapter{started: started, release: release, response: oldResponse}, store: store, id: id}
	ctx, cancel := context.WithCancel(context.Background())
	oldDone := make(chan error, 1)
	go func() {
		_, err := oldAdapter.Respond(ctx, llm.Request{Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "old"}}}}, llm.RequestOptions{})
		oldDone <- err
	}()
	<-started
	cancel()

	pending, err := store.LoadContextUsage(context.Background(), id)
	if err != nil || !pending.Pending || pending.RequestOrdinal != 1 {
		t.Fatalf("canceled request metadata=%+v err=%v", pending, err)
	}
	newResponse := llm.Response{ID: "new-response", Usage: llm.Usage{InputTokens: 29, OutputTokens: 7, Raw: json.RawMessage(`{"input_tokens":29,"output_tokens":7}`)}}
	next := &contextUsageAdapter{next: contextUsageFixtureAdapter{response: newResponse}, store: store, id: id, ordinal: pending.RequestOrdinal}
	if _, err := next.Respond(context.Background(), llm.Request{Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "new"}}}}, llm.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatalf("late adapter response returned %v", err)
	}
	latest, err := store.LoadContextUsage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Pending || latest.RequestOrdinal != 2 || latest.LatestProviderUsage.InputTokens == nil || *latest.LatestProviderUsage.InputTokens != 29 {
		gotInput := int64(-1)
		if latest.LatestProviderUsage.InputTokens != nil {
			gotInput = *latest.LatestProviderUsage.InputTokens
		}
		t.Fatalf("late canceled response overwrote latest metadata: ordinal=%d pending=%v input=%d", latest.RequestOrdinal, latest.Pending, gotInput)
	}
}

func TestContextUsageStoreRejectsCorruptOrOversizedMetadata(t *testing.T) {
	dir := t.TempDir()
	store := fileContextUsageStore{directory: dir}
	id := session.ID("bad-context-usage")
	if err := os.WriteFile(store.path(id), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadContextUsage(context.Background(), id); err == nil {
		t.Fatal("expected corrupt metadata error")
	}
	if err := os.WriteFile(store.path(id), make([]byte, maxContextUsageBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadContextUsage(context.Background(), id); err == nil {
		t.Fatal("expected oversized metadata error")
	}
}

func TestContextUsageRecordUsesProviderRawAvailability(t *testing.T) {
	usage := benchcontext.MeasureUsage(llm.Usage{Raw: json.RawMessage(`{"input_tokens":0,"output_tokens":null,"cache_read_input_tokens":0,"cache_creation_input_tokens":2}`)})
	if !usage.InputAvailable || usage.InputTokens != 0 || usage.OutputAvailable || !usage.CachedInputAvailable || usage.CachedInputTokens != 0 || !usage.CacheWriteAvailable || usage.CacheWriteTokens != 2 {
		t.Fatalf("usage presence=%+v", usage)
	}
	event := usageJSONEvent(session.ID("usage"), llm.Response{Usage: llm.Usage{Raw: json.RawMessage(`{"input_tokens":0,"output_tokens":null,"cache_read_input_tokens":0,"cache_creation_input_tokens":2}`)}})
	if event["input_tokens"] != int64(0) || event["input_tokens_available"] != true || event["cached_input_tokens"] != int64(0) || event["cache_write_input_tokens"] != int64(2) || event["output_tokens_available"] != false {
		t.Fatalf("usage event lost explicit availability: %#v", event)
	}
	if _, exists := event["output_tokens"]; exists {
		t.Fatalf("null output counter was emitted as zero: %#v", event)
	}
	unknown := usageJSONEvent(session.ID("usage"), llm.Response{Usage: llm.Usage{Raw: json.RawMessage(`{"vendor_metadata":1}`)}})
	if _, exists := unknown["input_tokens"]; exists || unknown["input_tokens_available"] != false {
		t.Fatalf("unknown usage was represented as zero: %#v", unknown)
	}
}

func BenchmarkContextUsageAdapterLocalSidecar(b *testing.B) {
	store := fileContextUsageStore{directory: b.TempDir()}
	adapter := &contextUsageAdapter{
		next:  contextUsageFixtureAdapter{response: llm.Response{Usage: llm.Usage{InputTokens: 128, OutputTokens: 16}}},
		store: store, id: "benchmark-context-usage",
	}
	request := llm.Request{
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: strings.Repeat("instruction ", 80)}}, {Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "measure local sidecar overhead"}}},
		Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "Bash", Description: "run commands", Parameters: map[string]any{"type": "object"}}},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := adapter.Respond(context.Background(), request, llm.RequestOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
