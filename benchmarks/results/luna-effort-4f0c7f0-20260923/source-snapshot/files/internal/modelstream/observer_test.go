package modelstream

import (
	"io"
	"strings"
	"testing"
)

func TestNewClientRequiresExplicitAccountID(t *testing.T) {
	_, err := NewClient(Config{AccessToken: "token"})
	if err == nil || !strings.Contains(err.Error(), "account ID must be set") {
		t.Fatalf("NewClient() error = %v, want explicit account ID requirement", err)
	}
}

func TestObservedSSEEmitsVisibleTextOnceAndSuppressesAnalysisAndArguments(t *testing.T) {
	var events []Event
	state := &callState{requestID: "request-1", callback: func(event Event) { events = append(events, event) }}
	body := &observedBody{
		ReadCloser:     io.NopCloser(strings.NewReader("")),
		state:          state,
		attempt:        1,
		assistantItems: make(map[string]bool),
		toolItems:      make(map[string]bool),
	}
	frames := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"id":"msg-final","type":"message","role":"assistant","phase":"final_answer"}}` + "\r\n\r\n",
		`data: {"type":"response.output_text.delta","item_id":"msg-final","delta":"hel"}` + "\n\n",
		`data: {"type":"response.output_text.delta","item_id":"msg-final","delta":"lo"}` + "\n\n",
		`data: {"type":"response.output_item.added","item":{"id":"msg-analysis","type":"message","role":"assistant","phase":"analysis"}}` + "\n\n",
		`data: {"type":"response.output_text.delta","item_id":"msg-analysis","delta":"do not show"}` + "\n\n",
		`data: {"type":"response.output_item.added","item":{"id":"call-1","type":"function_call","name":"Bash"}}` + "\n\n",
		`data: {"type":"response.function_call_arguments.delta","item_id":"unknown","delta":"secret"}` + "\n\n",
		`data: {"type":"response.function_call_arguments.delta","item_id":"call-1","delta":"{\"command\":\"secret\"}"}` + "\n\n",
		`data: {"type":"response.completed"}` + "\n\n",
	}, "")
	input := []byte(frames)
	for len(input) > 0 {
		n := min(7, len(input))
		body.consume(input[:n])
		input = input[n:]
	}

	var text []string
	var gotText, gotToolCount, gotComplete bool
	for _, event := range events {
		if event.RequestID != "request-1" || event.Attempt != 1 {
			t.Errorf("event correlation = %#v", event)
		}
		switch event.Kind {
		case EventAssistantDelta:
			gotText = true
			text = append(text, event.Text)
		case EventToolArgumentsProgress:
			gotToolCount = true
			if event.Text != "" || event.Bytes == 0 {
				t.Errorf("tool progress exposed args or omitted byte count: %#v", event)
			}
		case EventResponseCompleted:
			gotComplete = true
		}
		if strings.Contains(event.Text, "do not show") || strings.Contains(event.Text, "secret") {
			t.Errorf("unsafe text delta emitted: %#v", event)
		}
	}
	if !gotText || strings.Join(text, "") != "hello" {
		t.Fatalf("assistant delta text = %q, events %#v", strings.Join(text, ""), events)
	}
	if !gotToolCount || !gotComplete {
		t.Fatalf("missing safe tool progress or completion event: %#v", events)
	}
}

func TestObserverRetryDropsUnflushedPartialAttemptText(t *testing.T) {
	var events []Event
	state := &callState{requestID: "retry-id", callback: func(event Event) { events = append(events, event) }}
	state.emit(Event{Attempt: 1, Kind: EventAttemptStarted})
	state.emit(Event{Attempt: 1, Kind: EventAssistantDelta, ItemID: "message", Text: "partial old response", Bytes: len("partial old response")})
	state.emit(Event{Attempt: 2, Kind: EventAttemptStarted})
	state.emit(Event{Attempt: 2, Kind: EventAssistantDelta, ItemID: "message", Text: "final text", Bytes: len("final text")})
	state.flush(Event{Attempt: 2, Kind: EventResponseCompleted})

	var deltas []string
	for _, event := range events {
		if event.Kind == EventAssistantDelta {
			deltas = append(deltas, event.Text)
		}
	}
	if strings.Join(deltas, "") != "final text" {
		t.Fatalf("deltas after retry = %q, want only final attempt text", strings.Join(deltas, ""))
	}
}

func TestObserverBoundsAndDropsOversizedCompleteFrame(t *testing.T) {
	var events []Event
	body := &observedBody{
		ReadCloser:     io.NopCloser(strings.NewReader("")),
		state:          &callState{requestID: "large", callback: func(event Event) { events = append(events, event) }},
		attempt:        1,
		assistantItems: make(map[string]bool),
		toolItems:      make(map[string]bool),
	}
	body.assistantItems["message"] = true
	large := `data: {"type":"response.output_text.delta","item_id":"message","delta":"` + strings.Repeat("x", maxObservedSSEFrame) + `"}` + "\n\n"
	body.consume([]byte(large))
	if len(events) != 0 {
		t.Fatalf("oversized SSE frame produced telemetry: %#v", events)
	}
	if len(body.pending) > 3 {
		t.Fatalf("observer retained %d bytes after oversized frame", len(body.pending))
	}
}

func TestObserverCoalescesAndBoundsDraftText(t *testing.T) {
	var events []Event
	state := &callState{requestID: "bounded", callback: func(event Event) { events = append(events, event) }}
	state.emit(Event{Attempt: 1, Kind: EventAttemptStarted})
	chunk := strings.Repeat("x", 100)
	for range 1000 {
		state.emit(Event{Attempt: 1, Kind: EventAssistantDelta, ItemID: "message", Text: chunk, Bytes: len(chunk)})
	}
	state.flush(Event{Attempt: 1, Kind: EventResponseCompleted})
	deltaEvents, totalTextBytes := 0, 0
	for _, event := range events {
		if event.Kind == EventAssistantDelta {
			deltaEvents++
			totalTextBytes += len(event.Text)
		}
	}
	if deltaEvents > 2 {
		t.Fatalf("1000 tiny deltas produced %d UI events, want bounded/coalesced output", deltaEvents)
	}
	if totalTextBytes != maxDraftText {
		t.Fatalf("draft text bytes = %d, want cap %d", totalTextBytes, maxDraftText)
	}
}
