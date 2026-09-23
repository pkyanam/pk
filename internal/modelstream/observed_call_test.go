package modelstream

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestObservedCallTracksVisibleOutputAndFlushesBeforeCompletion(t *testing.T) {
	var events []Event
	ctx, produced := TrackOutput(WithObserver(context.Background(), func(event Event) {
		events = append(events, event)
	}))
	call, err := BeginObservedCall(ctx, "request-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	call.Emit(Event{Kind: EventAssistantDelta, ItemID: "message-1", Text: "first", Bytes: 5})
	call.Emit(Event{Kind: EventAssistantDelta, ItemID: "message-1", Text: "second", Bytes: 6})
	if !produced() {
		t.Fatal("visible partial output was not tracked")
	}
	call.Finish(nil)
	call.Finish(nil)

	var kinds []string
	for _, event := range events {
		if event.RequestID != "request-1" || event.Attempt != 3 {
			t.Fatalf("event missing request metadata: %+v", event)
		}
		kinds = append(kinds, event.Kind)
	}
	want := []string{EventAttemptStarted, EventAssistantDelta, EventResponseCompleted}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("event order = %v, want %v", kinds, want)
	}
	if events[1].Text != "firstsecond" || events[1].Bytes != 11 {
		t.Fatalf("coalesced output = %+v", events[1])
	}
}

func TestObservedCallHonorsWithoutObserverAndKeepsTrackOutput(t *testing.T) {
	called := false
	ctx, produced := TrackOutput(WithoutObserver(WithObserver(context.Background(), func(Event) { called = true })))
	call, err := BeginObservedCall(ctx, "internal-summary", 1)
	if err != nil {
		t.Fatal(err)
	}
	call.Emit(Event{Kind: EventAssistantDelta, Text: "hidden", Bytes: 6})
	call.Finish(nil)
	if called {
		t.Fatal("suppressed internal call reached the observer")
	}
	if !produced() {
		t.Fatal("TrackOutput did not observe suppressed-call output")
	}
}

func TestObservedCallFlushesReasoningBytesWithoutExposingText(t *testing.T) {
	var events []Event
	ctx := WithObserver(context.Background(), func(event Event) { events = append(events, event) })
	call, err := BeginObservedCall(ctx, "request-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	call.Emit(Event{Kind: EventReasoningProgress, Bytes: 9, Text: "must not be forwarded"})
	call.Finish(nil)
	if len(events) != 3 || events[1].Kind != EventReasoningProgress || events[1].Bytes != 9 || events[1].Text != "" {
		t.Fatalf("reasoning progress was not safely flushed: %+v", events)
	}
	if events[2].Kind != EventResponseCompleted {
		t.Fatalf("terminal event did not follow progress: %+v", events)
	}
}

func TestObservedCallOrdersPendingReasoningBeforeAssistantAndToolStart(t *testing.T) {
	for _, next := range []Event{
		{Kind: EventAssistantDelta, ItemID: "message", Text: "visible", Bytes: 7},
		{Kind: EventToolCallStarted, ItemID: "tool", ToolName: "Bash"},
	} {
		t.Run(next.Kind, func(t *testing.T) {
			var events []Event
			call, err := BeginObservedCall(WithObserver(context.Background(), func(event Event) {
				events = append(events, event)
			}), "request-order", 1)
			if err != nil {
				t.Fatal(err)
			}
			call.Emit(Event{Kind: EventReasoningProgress, Bytes: 12})
			call.Emit(next)
			call.Finish(nil)

			reasoningIndex, nextIndex := -1, -1
			for index, event := range events {
				if event.Kind == EventReasoningProgress {
					reasoningIndex = index
				}
				if event.Kind == next.Kind {
					nextIndex = index
				}
			}
			if reasoningIndex < 0 || nextIndex < 0 || reasoningIndex >= nextIndex {
				t.Fatalf("pending reasoning must precede %s: %+v", next.Kind, events)
			}
		})
	}
}

func TestProviderReasoningDeltasRequireOptInAndAreBoundedTransientOutput(t *testing.T) {
	var events []Event
	ctx, produced := TrackOutput(WithObserver(WithProviderReasoning(context.Background(), true), func(event Event) {
		events = append(events, event)
	}))
	call, err := BeginObservedCall(ctx, "provider-reasoning", 1)
	if err != nil {
		t.Fatal(err)
	}
	call.Emit(Event{Kind: EventProviderReasoningDelta, Text: strings.Repeat("x", maxProviderReasoningText+100)})
	call.Finish(nil)

	var reasoningText strings.Builder
	for _, event := range events {
		if event.Kind == EventProviderReasoningDelta {
			reasoningText.WriteString(event.Text)
		}
	}
	if reasoningText.Len() != maxProviderReasoningText {
		t.Fatalf("forwarded reasoning bytes=%d, want limit %d", reasoningText.Len(), maxProviderReasoningText)
	}
	if produced() {
		t.Fatal("provider reasoning counted as visible assistant output")
	}

	var suppressedEvents []Event
	suppressed, err := BeginObservedCall(WithoutObserver(WithObserver(WithProviderReasoning(context.Background(), true), func(event Event) {
		suppressedEvents = append(suppressedEvents, event)
	})), "suppressed-reasoning", 1)
	if err != nil {
		t.Fatal(err)
	}
	suppressed.Emit(Event{Kind: EventProviderReasoningDelta, Text: "private"})
	suppressed.Finish(nil)
	for _, event := range suppressedEvents {
		if event.Kind == EventProviderReasoningDelta {
			t.Fatalf("suppressed reasoning reached observer: %+v", event)
		}
	}
}
