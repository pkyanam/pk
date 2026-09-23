package modelstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type partialFailureAdapter struct{}

func (partialFailureAdapter) Respond(ctx context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	state := ctx.Value(callKey{}).(*callState)
	state.emit(Event{Attempt: 1, Kind: EventAttemptStarted})
	state.emit(Event{Attempt: 1, Kind: EventAssistantDelta, ItemID: "draft", Text: "unfinished", Bytes: 10})
	return llm.Response{}, errors.New("stream disconnected")
}

func TestClientFailureCancelsPendingProgress(t *testing.T) {
	events := make(chan Event, 8)
	client := &Client{adapter: partialFailureAdapter{}}
	_, err := client.Respond(WithObserver(context.Background(), func(event Event) { events <- event }), llm.Request{}, llm.RequestOptions{})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if got := <-events; got.Kind != EventAttemptStarted {
		t.Fatalf("first event: %s", got.Kind)
	}
	if got := <-events; got.Kind != EventRequestFailed {
		t.Fatalf("terminal event: %s", got.Kind)
	}
	select {
	case event := <-events:
		t.Fatalf("progress after request failure: %s", event.Kind)
	case <-time.After(2 * progressCoalesceInterval):
	}
}
