package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func TestSessionUsageSeparatesCompactionAttempts(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := localfile.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("summary-ledger")
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(ctx, id, session.Turn{ID: "turn", Type: session.TurnRegular}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendModelResponse(ctx, id, sessionstore.ModelResponse{TurnID: "turn", Response: llm.Response{Usage: llm.Usage{InputTokens: 100, OutputTokens: 20}}}); err != nil {
		t.Fatal(err)
	}
	before, err := readSessionUsage(ctx, store, string(id), dir)
	if err != nil || before.HistoryCompaction != nil {
		t.Fatalf("missing ledger should remain unavailable: %+v %v", before, err)
	}
	ledger := runner.NewLocalHistoryCompactionUsageStore(dir)
	if err := ledger.RecordHistoryCompactionAttempt(ctx, string(id), llm.Response{Stop: llm.StopComplete, Usage: llm.Usage{InputTokens: 30, OutputTokens: 5}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordHistoryCompactionAttempt(ctx, string(id), llm.Response{}, errors.New("provider failed")); err != nil {
		t.Fatal(err)
	}
	got, err := readSessionUsage(ctx, store, string(id), dir)
	if err != nil {
		t.Fatal(err)
	}
	assertUsageTotal(t, got.InputTokens, 100)
	assertUsageTotal(t, got.OutputTokens, 20)
	summary := got.HistoryCompaction
	if got.ResponseCount != 1 || summary == nil || summary.Attempts != 2 || summary.Failed != 1 || summary.UnknownUsageAttempts != 1 || summary.InputCalls != 1 || summary.OutputCalls != 1 {
		t.Fatalf("ledger=%+v ordinary=%+v", summary, got)
	}
	assertUsageTotal(t, summary.InputTokens, 30)
	assertUsageTotal(t, summary.OutputTokens, 5)
	if summary.CachedInputTokens != nil {
		t.Fatal("missing summary cache count became zero")
	}
}
