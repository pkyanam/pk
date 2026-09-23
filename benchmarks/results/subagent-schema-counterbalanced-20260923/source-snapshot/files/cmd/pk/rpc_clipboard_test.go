package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/clipboard"
)

type clipboardWriterFixture struct {
	text  string
	err   error
	calls int
}

func (f *clipboardWriterFixture) WriteText(_ context.Context, text string) error {
	f.calls++
	f.text = text
	return f.err
}

func TestRPCClipboardWriteAcknowledgesOnlyAfterNativeWrite(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	writer := &clipboardWriterFixture{}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, requestTypes: map[string]string{}, clipboardWriter: writer}
	server.handle(rpcMessage{Version: 1, ID: "copy-1", Type: "clipboard_write", Payload: json.RawMessage(`{"text":"selected text"}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "clipboard_written" || event.ID != "copy-1" || event.Payload.(map[string]any)["bytes"] != float64(len("selected text")) {
		t.Fatalf("clipboard acknowledgement=%+v", event)
	}
	if writer.calls != 1 || writer.text != "selected text" {
		t.Fatalf("native writer calls=%d text=%q", writer.calls, writer.text)
	}

	writer.err = errors.New("clipboard unavailable")
	server.handle(rpcMessage{Version: 1, ID: "copy-2", Type: "clipboard_write", Payload: json.RawMessage(`{"text":"retry with terminal"}`)}, make(chan turnDone, 1))
	event = readRPCEvent(t, sink)
	if event.Type != "error" || !strings.Contains(event.Payload.(map[string]any)["message"].(string), "clipboard unavailable") {
		t.Fatalf("failed write event=%+v", event)
	}
}

func TestRPCClipboardWriteRejectsOversizedTextBeforeNativeCall(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 2)}
	writer := &clipboardWriterFixture{}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, requestTypes: map[string]string{}, clipboardWriter: writer}
	text := strings.Repeat("x", clipboard.MaxTextBytes+1)
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	server.handle(rpcMessage{Version: 1, ID: "copy-large", Type: "clipboard_write", Payload: payload}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "error" || writer.calls != 0 {
		t.Fatalf("oversized clipboard event=%+v writer calls=%d", event, writer.calls)
	}
}
