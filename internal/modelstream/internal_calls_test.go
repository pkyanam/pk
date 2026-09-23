package modelstream

import (
	"context"
	"errors"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type observerCheckAdapter struct{ inspect func(context.Context) }

func (adapter observerCheckAdapter) Respond(ctx context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	adapter.inspect(ctx)
	return llm.Response{}, nil
}

func TestInternalSummarySuppressesObserverAndKeepsCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := WithoutObserver(WithObserver(parent, func(Event) { t.Error("internal summary leaked into UI") }))
	// The RPC progress adapter installs its observer inside Respond. Suppression
	// must survive that later wrapper, rather than only clearing the outer one.
	ctx = WithObserver(ctx, func(Event) { t.Error("inner adapter re-enabled summary streaming") })
	client := &Client{adapter: observerCheckAdapter{inspect: func(ctx context.Context) {
		if ctx.Value(callKey{}) != nil {
			t.Error("summary request created a streaming UI observer")
		}
	}}}
	if _, err := client.Respond(ctx, llm.Request{}, llm.RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := client.Respond(ctx, llm.Request{}, llm.RequestOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestOutputTrackerSeesFailedCoalescedDelta(t *testing.T) {
	var sawFailure bool
	ctx, produced := TrackOutput(WithObserver(context.Background(), func(event Event) {
		if event.Kind == EventAssistantDelta {
			t.Error("failed pending delta should not be shown")
		}
		if event.Kind == EventRequestFailed {
			sawFailure = true
		}
	}))
	// Model progress wrappers may replace the visible callback after tracking is
	// installed; that must not remove the internal output guard.
	previous := ctx.Value(observerKey{}).(observerConfig).callback
	ctx = WithObserver(ctx, previous)
	client := &Client{adapter: partialFailureAdapter{}}
	if _, err := client.Respond(ctx, llm.Request{}, llm.RequestOptions{}); err == nil {
		t.Fatal("expected provider failure")
	}
	if !produced() || !sawFailure {
		t.Fatalf("output tracker=%v, original observer failure=%v", produced(), sawFailure)
	}
}

func TestOutputTrackerWithoutUIAndIndependentRequests(t *testing.T) {
	for _, kind := range []string{EventAttemptStarted, EventToolCallStarted, EventToolArgumentsProgress, EventToolCallReady} {
		t.Run(kind, func(t *testing.T) {
			ctx, produced := TrackOutput(context.Background())
			client := &Client{adapter: observerCheckAdapter{inspect: func(ctx context.Context) {
				ctx.Value(callKey{}).(*callState).emit(Event{Kind: kind})
			}}}
			if _, err := client.Respond(ctx, llm.Request{}, llm.RequestOptions{}); err != nil {
				t.Fatal(err)
			}
			if produced() != (kind != EventAttemptStarted) {
				t.Fatalf("unexpected output state for %s: %v", kind, produced())
			}
		})
	}
}
