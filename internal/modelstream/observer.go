package modelstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const maxObservedSSEFrame = 64 << 10
const maxTrackedItems = 1024
const maxDeltaText = 16 << 10
const maxDraftText = 64 << 10
const progressCoalesceInterval = 75 * time.Millisecond

type observingTransport struct{ base http.RoundTripper }

func (transport *observingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	state, _ := request.Context().Value(callKey{}).(*callState)
	if state == nil {
		return transport.base.RoundTrip(request)
	}
	attempt := int(state.attempt.Add(1))
	state.emit(Event{Attempt: attempt, Kind: EventAttemptStarted})
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		state.emit(Event{Attempt: attempt, Kind: EventAttemptFailed})
		return response, err
	}
	state.emit(Event{Attempt: attempt, Status: response.StatusCode, Kind: EventResponseStarted})
	response.Body = &observedBody{ReadCloser: response.Body, state: state, attempt: attempt, assistantItems: make(map[string]bool), toolItems: make(map[string]bool)}
	return response, nil
}

func (transport *observingTransport) CloseIdleConnections() {
	if closer, ok := transport.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type observedBody struct {
	io.ReadCloser
	state          *callState
	attempt        int
	assistantItems map[string]bool
	toolItems      map[string]bool
	pending        []byte
	discarding     bool
}

func (body *observedBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if n > 0 {
		body.consume(buffer[:n])
	}
	return n, err
}

func (body *observedBody) consume(chunk []byte) {
	body.pending = append(body.pending, chunk...)
	for {
		index, delimiter := frameDelimiter(body.pending)
		if index < 0 {
			break
		}
		frame := body.pending[:index]
		body.pending = body.pending[index+delimiter:]
		if body.discarding || len(frame) > maxObservedSSEFrame {
			body.discarding = false
			continue
		}
		body.observeFrame(frame)
	}
	if len(body.pending) > maxObservedSSEFrame {
		body.pending = append(body.pending[:0], body.pending[len(body.pending)-3:]...)
		body.discarding = true
	}
}

func frameDelimiter(value []byte) (int, int) {
	lf := bytes.Index(value, []byte("\n\n"))
	crlf := bytes.Index(value, []byte("\r\n\r\n"))
	if crlf >= 0 && (lf < 0 || crlf < lf) {
		return crlf, 4
	}
	if lf >= 0 {
		return lf, 2
	}
	return -1, 0
}

func (body *observedBody) observeFrame(frame []byte) {
	var data []string
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimPrefix(line, []byte("data:"))
			value = bytes.TrimPrefix(value, []byte(" "))
			data = append(data, string(value))
		}
	}
	if len(data) == 0 {
		return
	}
	var event struct {
		Type   string `json:"type"`
		ItemID string `json:"item_id"`
		Delta  string `json:"delta"`
		Item   struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Role  string `json:"role"`
			Phase string `json:"phase"`
			Name  string `json:"name"`
		} `json:"item"`
	}
	if json.Unmarshal([]byte(strings.Join(data, "\n")), &event) != nil {
		return
	}
	switch event.Type {
	case "response.output_item.added":
		itemID := event.Item.ID
		if itemID == "" {
			itemID = event.ItemID
		}
		if itemID == "" || len(body.assistantItems)+len(body.toolItems) >= maxTrackedItems {
			return
		}
		if event.Item.Type == "message" && event.Item.Role == "assistant" {
			body.assistantItems[itemID] = event.Item.Phase == "" || event.Item.Phase == "commentary" || event.Item.Phase == "final_answer"
		} else if event.Item.Type == "function_call" || event.Item.Type == "custom_tool_call" {
			body.toolItems[itemID] = true
			body.state.emit(Event{Attempt: body.attempt, Kind: EventToolCallStarted, ItemID: itemID, ToolName: event.Item.Name})
		}
	case "response.output_text.delta":
		if event.ItemID == "" || !body.assistantItems[event.ItemID] || event.Delta == "" {
			return
		}
		text := event.Delta
		if len(text) > maxDeltaText {
			text = text[:maxDeltaText]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		body.state.emit(Event{Attempt: body.attempt, Kind: EventAssistantDelta, ItemID: event.ItemID, Text: text, Bytes: len(event.Delta)})
	case "response.function_call_arguments.delta":
		if event.ItemID != "" && body.toolItems[event.ItemID] && event.Delta != "" {
			body.state.emit(Event{Attempt: body.attempt, Kind: EventToolArgumentsProgress, ItemID: event.ItemID, Bytes: len(event.Delta)})
		}
	case "response.output_item.done":
		itemID := event.Item.ID
		if itemID == "" {
			itemID = event.ItemID
		}
		if itemID != "" && (event.Item.Type == "function_call" || event.Item.Type == "custom_tool_call") {
			body.state.emit(Event{Attempt: body.attempt, Kind: EventToolCallReady, ItemID: itemID, ToolName: event.Item.Name})
		}
	case "response.incomplete":
		body.state.flush(Event{Attempt: body.attempt, Kind: EventResponseIncomplete})
	case "response.failed":
		body.state.flush(Event{Attempt: body.attempt, Kind: EventResponseFailed})
	case "response.completed":
		body.state.flush(Event{Attempt: body.attempt, Kind: EventResponseCompleted})
	}
}

func (state *callState) emit(event Event) {
	if state != nil && state.onOutput != nil {
		switch event.Kind {
		case EventAssistantDelta, EventToolCallStarted, EventToolArgumentsProgress, EventToolCallReady:
			state.onOutput()
		}
	}
	if state == nil || state.callback == nil {
		return
	}
	state.deliveryMu.Lock()
	defer state.deliveryMu.Unlock()
	if event.Kind == EventAssistantDelta {
		state.mu.Lock()
		var pending []Event
		if state.pendingID != "" && state.pendingID != event.ItemID {
			pending = append(pending, state.takeTextLocked(event.Attempt)...)
		}
		state.pendingID = event.ItemID
		room := maxDraftText - state.draftUsed
		if room > 0 {
			text := event.Text
			if len(text) > room {
				text = text[:room]
				for !utf8.ValidString(text) {
					text = text[:len(text)-1]
				}
			}
			state.pending.WriteString(text)
			state.draftUsed += len(text)
		}
		state.pendingBytes += event.Bytes
		ready := time.Since(state.lastEmit) >= progressCoalesceInterval
		if ready {
			state.stopFlushTimerLocked()
			pending = append(pending, state.takeReasoningBytesLocked(event.Attempt)...)
			pending = append(pending, state.takeTextLocked(event.Attempt)...)
			pending = append(pending, state.takeToolBytesLocked(event.Attempt)...)
			state.lastEmit = time.Now()
		} else {
			state.scheduleFlushLocked(event.Attempt)
		}
		state.mu.Unlock()
		state.send(pending...)
		return
	}
	if event.Kind == EventToolArgumentsProgress {
		state.mu.Lock()
		if state.toolBytes == nil {
			state.toolBytes = make(map[string]int)
		}
		state.toolBytes[event.ItemID] += event.Bytes
		ready := time.Since(state.lastEmit) >= progressCoalesceInterval
		var pending []Event
		if ready {
			state.stopFlushTimerLocked()
			pending = append(pending, state.takeReasoningBytesLocked(event.Attempt)...)
			pending = append(pending, state.takeTextLocked(event.Attempt)...)
			pending = append(pending, state.takeToolBytesLocked(event.Attempt)...)
			state.lastEmit = time.Now()
		} else {
			state.scheduleFlushLocked(event.Attempt)
		}
		state.mu.Unlock()
		state.send(pending...)
		return
	}
	if event.Kind == EventReasoningProgress {
		state.mu.Lock()
		state.pendingReasoningBytes += event.Bytes
		ready := time.Since(state.lastEmit) >= progressCoalesceInterval
		var pending []Event
		if ready {
			state.stopFlushTimerLocked()
			pending = append(pending, state.takeReasoningBytesLocked(event.Attempt)...)
			pending = append(pending, state.takeTextLocked(event.Attempt)...)
			pending = append(pending, state.takeToolBytesLocked(event.Attempt)...)
			state.lastEmit = time.Now()
		} else {
			state.scheduleFlushLocked(event.Attempt)
		}
		state.mu.Unlock()
		state.send(pending...)
		return
	}
	if event.Kind == EventAttemptStarted {
		state.mu.Lock()
		state.stopFlushTimerLocked()
		state.pending.Reset()
		state.pendingBytes = 0
		state.pendingID = ""
		state.draftUsed = 0
		state.toolBytes = nil
		state.pendingReasoningBytes = 0
		state.lastEmit = time.Now()
		state.mu.Unlock()
	}
	if event.Kind == EventRequestFailed || event.Kind == EventAttemptFailed {
		state.mu.Lock()
		state.stopFlushTimerLocked()
		state.pending.Reset()
		state.pendingID = ""
		state.pendingBytes = 0
		state.toolBytes = nil
		state.pendingReasoningBytes = 0
		state.mu.Unlock()
	}
	if event.Kind == EventToolCallStarted {
		state.mu.Lock()
		state.stopFlushTimerLocked()
		pending := state.takeReasoningBytesLocked(event.Attempt)
		pending = append(pending, state.takeTextLocked(event.Attempt)...)
		pending = append(pending, state.takeToolBytesLocked(event.Attempt)...)
		state.mu.Unlock()
		state.send(pending...)
	}
	state.send(event)
}

func (state *callState) flush(final Event) {
	state.deliveryMu.Lock()
	defer state.deliveryMu.Unlock()
	state.mu.Lock()
	state.stopFlushTimerLocked()
	pending := state.takeReasoningBytesLocked(final.Attempt)
	pending = append(pending, state.takeTextLocked(final.Attempt)...)
	pending = append(pending, state.takeToolBytesLocked(final.Attempt)...)
	state.mu.Unlock()
	pending = append(pending, final)
	state.send(pending...)
}

func (state *callState) scheduleFlushLocked(attempt int) {
	if state.flushTimer != nil {
		return
	}
	state.flushVersion++
	version := state.flushVersion
	delay := progressCoalesceInterval - time.Since(state.lastEmit)
	if delay < 0 {
		delay = 0
	}
	state.flushTimer = time.AfterFunc(delay, func() {
		state.deliveryMu.Lock()
		defer state.deliveryMu.Unlock()
		state.mu.Lock()
		if state.flushVersion != version {
			state.mu.Unlock()
			return
		}
		state.flushTimer = nil
		pending := state.takeReasoningBytesLocked(attempt)
		pending = append(pending, state.takeTextLocked(attempt)...)
		pending = append(pending, state.takeToolBytesLocked(attempt)...)
		if len(pending) > 0 {
			state.lastEmit = time.Now()
		}
		state.mu.Unlock()
		state.send(pending...)
	})
}

func (state *callState) takeReasoningBytesLocked(attempt int) []Event {
	if state.pendingReasoningBytes == 0 {
		return nil
	}
	bytes := state.pendingReasoningBytes
	state.pendingReasoningBytes = 0
	return []Event{{Attempt: attempt, Kind: EventReasoningProgress, Bytes: bytes}}
}

func (state *callState) stopFlushTimerLocked() {
	state.flushVersion++
	if state.flushTimer != nil {
		state.flushTimer.Stop()
		state.flushTimer = nil
	}
}

func (state *callState) takeTextLocked(attempt int) []Event {
	if state.pending.Len() == 0 {
		state.pendingID = ""
		state.pendingBytes = 0
		return nil
	}
	event := Event{Attempt: attempt, Kind: EventAssistantDelta, ItemID: state.pendingID, Text: state.pending.String(), Bytes: state.pendingBytes}
	state.pending.Reset()
	state.pendingBytes = 0
	state.pendingID = ""
	return []Event{event}
}

func (state *callState) takeToolBytesLocked(attempt int) []Event {
	result := make([]Event, 0, len(state.toolBytes))
	for itemID, count := range state.toolBytes {
		result = append(result, Event{Attempt: attempt, Kind: EventToolArgumentsProgress, ItemID: itemID, Bytes: count})
	}
	state.toolBytes = nil
	return result
}

func (state *callState) send(events ...Event) {
	for _, event := range events {
		event.RequestID = state.requestID
		func() {
			defer func() { _ = recover() }()
			state.callback(event)
		}()
	}
}

func withRequestObserver(ctx context.Context, requestID string) context.Context {
	config, ok := ctx.Value(observerKey{}).(observerConfig)
	if !ok || config.callback == nil {
		return ctx
	}
	return context.WithValue(ctx, callKey{}, &callState{requestID: requestID, callback: config.callback})
}
