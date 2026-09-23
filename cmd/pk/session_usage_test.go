package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func TestReadSessionUsageAggregatesDurableResponsesAndCoverage(t *testing.T) {
	ctx := context.Background()
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("usage-session")
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	responses := []llm.Response{
		{ID: "duplicate-id", Usage: llm.Usage{Raw: jsontext.Value(`{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":25}}`)}},
		{ID: "duplicate-id", Usage: llm.Usage{Raw: jsontext.Value(`{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}`)}},
		{ID: "third", Usage: llm.Usage{Raw: jsontext.Value(`{"input_tokens":50,"output_tokens":10,"input_tokens_details":{"cached_tokens":75}}`)}},
		{ID: "fourth", Usage: llm.Usage{Raw: jsontext.Value(`{"output_tokens":5}`)}},
	}
	previousTurn := session.TurnID("")
	for i, response := range responses {
		turnID := session.TurnID("turn-" + string(rune('1'+i)))
		if err := store.AppendTurn(ctx, id, session.Turn{ID: turnID, PreviousTurnID: previousTurn, Type: session.TurnRegular}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendModelResponse(ctx, id, sessionstore.ModelResponse{TurnID: turnID, Response: response}); err != nil {
			t.Fatal(err)
		}
		previousTurn = turnID
	}
	got, err := readSessionUsage(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseCount != 4 {
		t.Fatalf("response_count=%d", got.ResponseCount)
	}
	assertUsageTotal(t, got.InputTokens, 150)
	assertUsageTotal(t, got.OutputTokens, 35)
	assertUsageTotal(t, got.CachedInputTokens, 100)
	assertUsageTotal(t, got.UncachedInputTokens, 75)
	if got.Coverage != (sessionUsageCoverage{InputResponses: 3, OutputResponses: 4, CachedInputResponses: 3, UncachedResponses: 2}) {
		t.Fatalf("coverage=%+v", got.Coverage)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || strings.Contains(string(encoded), "reasoning") || strings.Contains(string(encoded), "prompt") {
		t.Fatalf("unexpected serialized usage response: %s", encoded)
	}
}

func TestReadSessionUsageLeavesUnknownCountersUnavailable(t *testing.T) {
	ctx := context.Background()
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("usage-missing")
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	previousTurn := session.TurnID("")
	for i, usage := range []llm.Usage{{Raw: jsontext.Value(`{}`)}, {Raw: jsontext.Value(`{"input_tokens":-1,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}`)}} {
		turnID := session.TurnID("turn-" + string(rune('1'+i)))
		if err := store.AppendTurn(ctx, id, session.Turn{ID: turnID, PreviousTurnID: previousTurn, Type: session.TurnRegular}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendModelResponse(ctx, id, sessionstore.ModelResponse{TurnID: turnID, Response: llm.Response{Usage: usage}}); err != nil {
			t.Fatal(err)
		}
		previousTurn = turnID
	}
	got, err := readSessionUsage(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseCount != 2 || got.InputTokens != nil || got.CachedInputTokens == nil || got.OutputTokens == nil || got.UncachedInputTokens != nil {
		t.Fatalf("summary=%+v", got)
	}
	if *got.CachedInputTokens != 3 || *got.OutputTokens != 2 || got.Coverage.UncachedResponses != 0 {
		t.Fatalf("summary=%+v", got)
	}
}

func TestReadSessionUsageTreatsNullAndMalformedAsUnavailableAndSupportsChatAliases(t *testing.T) {
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, id, appendResponse := newUsageSession(t, store, "usage-null")
	appendResponse(llm.Usage{InputTokens: 42, OutputTokens: 43, CachedInputTokens: 44, Raw: jsontext.Value(`{"input_tokens":null,"output_tokens":"bad","input_tokens_details":{"cached_tokens":null}}`)})
	appendResponse(llm.Usage{Raw: jsontext.Value(`{"prompt_tokens":0,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}`)})
	got, err := readSessionUsage(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageTotal(t, got.InputTokens, 0)
	assertUsageTotal(t, got.OutputTokens, 0)
	assertUsageTotal(t, got.CachedInputTokens, 0)
	assertUsageTotal(t, got.UncachedInputTokens, 0)
	if got.Coverage != (sessionUsageCoverage{InputResponses: 1, OutputResponses: 1, CachedInputResponses: 1, UncachedResponses: 1}) {
		t.Fatalf("coverage=%+v", got.Coverage)
	}
}

func TestReadSessionUsageAggregatesAnthropicNativeInputAndCacheCounters(t *testing.T) {
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, id, appendResponse := newUsageSession(t, store, "usage-anthropic")
	appendResponse(llm.Usage{
		InputTokens: 17, CachedInputTokens: 3, CacheWriteInputTokens: 2, OutputTokens: 7,
		Raw: jsontext.Value(`{"input_tokens":12,"cache_read_input_tokens":3,"cache_creation_input_tokens":2,"output_tokens":7}`),
	})
	appendResponse(llm.Usage{
		InputTokens: 0, CachedInputTokens: 0, CacheWriteInputTokens: 0, OutputTokens: 0,
		Raw: jsontext.Value(`{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}`),
	})
	got, err := readSessionUsage(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseCount != 2 {
		t.Fatalf("summary=%+v", got)
	}
	assertUsageTotal(t, got.InputTokens, 17)
	assertUsageTotal(t, got.OutputTokens, 7)
	assertUsageTotal(t, got.CachedInputTokens, 3)
	// The existing uncached convention is total input minus cache reads, so
	// cache-creation tokens remain included in this total.
	assertUsageTotal(t, got.UncachedInputTokens, 14)
	if got.Coverage != (sessionUsageCoverage{InputResponses: 2, OutputResponses: 2, CachedInputResponses: 2, UncachedResponses: 2}) {
		t.Fatalf("coverage=%+v", got.Coverage)
	}
}

func TestReadSessionUsageKeepsIncompleteAnthropicCountersUnavailable(t *testing.T) {
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, id, appendResponse := newUsageSession(t, store, "usage-anthropic-incomplete")
	appendResponse(llm.Usage{
		InputTokens: 9, CachedInputTokens: 0, OutputTokens: 4,
		Raw: jsontext.Value(`{"input_tokens":9,"cache_read_input_tokens":null,"cache_creation_input_tokens":0,"output_tokens":4}`),
	})
	appendResponse(llm.Usage{
		InputTokens: 8, CachedInputTokens: 2, OutputTokens: 3,
		Raw: jsontext.Value(`{"input_tokens":5,"cache_read_input_tokens":2,"output_tokens":3}`),
	})
	got, err := readSessionUsage(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != nil || got.UncachedInputTokens != nil {
		t.Fatalf("incomplete native counters should not become totals: %+v", got)
	}
	assertUsageTotal(t, got.CachedInputTokens, 2)
	assertUsageTotal(t, got.OutputTokens, 7)
	if got.Coverage != (sessionUsageCoverage{OutputResponses: 2, CachedInputResponses: 1}) {
		t.Fatalf("coverage=%+v", got.Coverage)
	}
}

func TestReadSessionUsageReadsBeyond256ItemsAndHonorsCancellation(t *testing.T) {
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, id, appendResponse := newUsageSession(t, store, "usage-many")
	for i := 0; i < 260; i++ {
		appendResponse(llm.Usage{Raw: jsontext.Value(`{"input_tokens":1}`)})
	}
	got, err := readSessionUsage(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseCount != 260 || got.InputTokens == nil || *got.InputTokens != 260 || got.Coverage.InputResponses != 260 {
		t.Fatalf("summary=%+v", got)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := readSessionUsage(canceled, store, string(id)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read err=%v", err)
	}
}

func TestReadSessionUsageOverflowLeavesAggregateUnavailable(t *testing.T) {
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, id, appendResponse := newUsageSession(t, store, "usage-overflow")
	appendResponse(llm.Usage{Raw: jsontext.Value(`{"input_tokens":9223372036854775807}`)})
	appendResponse(llm.Usage{Raw: jsontext.Value(`{"input_tokens":1}`)})
	got, err := readSessionUsage(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != nil || got.Coverage.InputResponses != 2 {
		t.Fatalf("summary=%+v", got)
	}
}

func TestRPCSessionUsageReadsSavedSession(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("saved-usage")
	if _, err := store.Create(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, sessionDir: sessionDir, requestTypes: make(map[string]string)}
	server.handle(rpcMessage{Version: 1, ID: "usage-1", Type: "session_usage", Payload: []byte(`{"session_id":"saved-usage"}`)}, make(chan turnDone, 1))
	for i := 0; i < 2; i++ {
		select {
		case raw := <-sink.events:
			var event struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Payload struct {
					SessionID     string `json:"session_id"`
					ResponseCount int    `json:"response_count"`
					InputTokens   *int64 `json:"input_tokens"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "session_usage" {
				if event.ID != "usage-1" || event.Payload.SessionID != "saved-usage" || event.Payload.ResponseCount != 0 || event.Payload.InputTokens != nil {
					t.Fatalf("event=%+v", event)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("session usage response timed out")
		}
	}
	t.Fatal("session_usage event not received")
}

func TestRPCSessionUsageCancelRequiresOriginalRequestID(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, requestTypes: make(map[string]string)}
	started := make(chan struct{})
	server.startSkillOperation("usage-original", "session usage", "session_usage_started", "session_usage", nil, func(ctx context.Context) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	<-started
	server.handle(rpcMessage{Version: 1, ID: "cancel-wrong", Type: "session_usage_cancel", Payload: []byte(`{"request_id":"stale"}`)}, make(chan turnDone, 1))
	var wrong struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(<-sink.events, &wrong); err != nil {
		t.Fatal(err)
	}
	if wrong.Type != "session_usage_started" {
		t.Fatalf("first event=%+v", wrong)
	}
	if err := json.Unmarshal(<-sink.events, &wrong); err != nil {
		t.Fatal(err)
	}
	if wrong.Type != "error" {
		t.Fatalf("stale cancel event=%+v", wrong)
	}
	server.handle(rpcMessage{Version: 1, ID: "cancel-correct", Type: "session_usage_cancel", Payload: []byte(`{"request_id":"usage-original"}`)}, make(chan turnDone, 1))
	seenCancelEvent, seenOperationError := false, false
	for !seenCancelEvent || !seenOperationError {
		select {
		case raw := <-sink.events:
			var event struct {
				Type    string `json:"type"`
				Payload struct {
					Cancelled bool `json:"cancelled"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			seenCancelEvent = seenCancelEvent || event.Type == "session_usage_cancel_requested"
			seenOperationError = seenOperationError || event.Type == "error" && event.Payload.Cancelled
		case <-time.After(time.Second):
			t.Fatal("matching usage cancel did not settle")
		}
	}
}

func assertUsageTotal(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("total=%v want %d", got, want)
	}
}

func newUsageSession(t *testing.T, store *localfile.Store, name string) (context.Context, session.ID, func(llm.Usage)) {
	t.Helper()
	ctx := context.Background()
	id := session.ID(name)
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	previousTurn := session.TurnID("")
	index := 0
	appendResponse := func(usage llm.Usage) {
		turnID := session.TurnID("turn-" + strconv.Itoa(index))
		index++
		if err := store.AppendTurn(ctx, id, session.Turn{ID: turnID, PreviousTurnID: previousTurn, Type: session.TurnRegular}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendModelResponse(ctx, id, sessionstore.ModelResponse{TurnID: turnID, Response: llm.Response{Usage: usage}}); err != nil {
			t.Fatal(err)
		}
		previousTurn = turnID
	}
	return ctx, id, appendResponse
}
