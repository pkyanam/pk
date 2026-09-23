package modelstream

import (
	"sync"
	"testing"
	"time"
)

func TestSingleTextDeltaFlushesDuringProviderPause(t *testing.T) {
	events := make(chan Event, 4)
	state := &callState{requestID: "pause-test", callback: func(event Event) { events <- event }}
	state.emit(Event{Attempt: 1, Kind: EventAttemptStarted})
	<-events
	state.emit(Event{Attempt: 1, Kind: EventAssistantDelta, ItemID: "message", Text: "visible first words", Bytes: len("visible first words")})

	select {
	case event := <-events:
		if event.Kind != EventAssistantDelta || event.Text != "visible first words" {
			t.Fatalf("event during provider pause = %#v", event)
		}
	case <-time.After(progressCoalesceInterval + 100*time.Millisecond):
		t.Fatal("pending visible text was not emitted until another provider event arrived")
	}

	state.flush(Event{Attempt: 1, Kind: EventResponseCompleted})
	select {
	case event := <-events:
		if event.Kind != EventResponseCompleted {
			t.Fatalf("terminal event = %#v, want response_completed", event)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal response event was not delivered")
	}
}

func TestTimerFlushCannotDeliverDraftAfterTerminalEvent(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var delivered []string
	state := &callState{requestID: "terminal-order", callback: func(event Event) {
		if event.Kind == EventAssistantDelta {
			close(started)
			<-release
		}
		mu.Lock()
		delivered = append(delivered, event.Kind)
		mu.Unlock()
	}}
	state.emit(Event{Attempt: 1, Kind: EventAttemptStarted})
	state.emit(Event{Attempt: 1, Kind: EventAssistantDelta, ItemID: "message", Text: "draft", Bytes: 5})

	select {
	case <-started:
	case <-time.After(progressCoalesceInterval + time.Second):
		t.Fatal("timer did not begin draft delivery")
	}
	terminalDone := make(chan struct{})
	go func() {
		state.flush(Event{Attempt: 1, Kind: EventResponseCompleted})
		close(terminalDone)
	}()
	select {
	case <-terminalDone:
		t.Fatal("terminal event passed an in-flight draft delivery")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-terminalDone:
	case <-time.After(time.Second):
		t.Fatal("terminal event delivery did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 3 || delivered[0] != EventAttemptStarted || delivered[1] != EventAssistantDelta || delivered[2] != EventResponseCompleted {
		t.Fatalf("delivery order = %v", delivered)
	}
}
