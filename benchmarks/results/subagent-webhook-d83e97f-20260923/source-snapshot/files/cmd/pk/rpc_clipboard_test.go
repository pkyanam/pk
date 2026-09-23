package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/clipboard"
	"github.com/pkyanam/pk/internal/runner"
)

type clipboardWriterFixture struct {
	text  string
	err   error
	calls int
}

type blockingClipboardWriter struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

type blockingClipboardProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingClipboardProvider) unblock() { p.once.Do(func() { close(p.release) }) }

func (p *blockingClipboardProvider) Read(context.Context) (clipboard.Snapshot, error) {
	close(p.started)
	<-p.release
	return clipboard.Snapshot{Text: "late result"}, nil
}

func (w *blockingClipboardWriter) unblock() { w.once.Do(func() { close(w.release) }) }

func (w *blockingClipboardWriter) WriteText(ctx context.Context, _ string) error {
	w.calls.Add(1)
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		// Model native AppKit behavior: context cancellation is not observed
		// once a native clipboard call has entered.
		<-w.release
		return nil
	}
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

func TestRPCClipboardTimeoutDoesNotBlockRequestsOrStartLateWrites(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	writer := &blockingClipboardWriter{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(writer.unblock)
	server := &rpcServer{
		ctx: context.Background(), output: sink, started: true,
		requestTypes: make(map[string]string), clipboardWriter: writer,
		clipboardTimeout: 150 * time.Millisecond,
	}
	finished := make(chan turnDone, 1)
	startedAt := time.Now()
	handleReturned := make(chan struct{})
	go func() {
		server.handle(rpcMessage{Version: 1, ID: "copy-1", Type: "clipboard_write", Payload: json.RawMessage(`{"text":"first"}`)}, finished)
		close(handleReturned)
	}()
	select {
	case <-handleReturned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("clipboard handler blocked the RPC reader loop")
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("clipboard handle blocked RPC loop for %s", elapsed)
	}
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("clipboard writer did not start")
	}

	// A separate request is still serviced while the native write is stalled.
	server.handle(rpcMessage{Version: 1, ID: "status-1", Type: "status"}, finished)
	readFor := func(id string) rpcEvent {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case raw := <-sink.events:
				var event rpcEvent
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				if event.ID == id {
					return event
				}
			case <-deadline:
				t.Fatalf("timed out waiting for RPC event %q", id)
				return rpcEvent{}
			}
		}
	}
	if event := readFor("status-1"); event.Type != "status" || event.ID != "status-1" {
		t.Fatalf("unrelated request blocked by clipboard call: %+v", event)
	}

	// The single operation slot prevents a later write from racing a timed-out
	// AppKit call that may still complete and overwrite the user's clipboard.
	server.handle(rpcMessage{Version: 1, ID: "copy-2", Type: "clipboard_write", Payload: json.RawMessage(`{"text":"second"}`)}, finished)
	busy := readFor("copy-2")
	if busy.Type != "error" || busy.ID != "copy-2" || busy.Payload.(map[string]any)["clipboard_busy"] != true || busy.Payload.(map[string]any)["native_may_complete_late"] != true {
		t.Fatalf("busy clipboard response = %+v", busy)
	}
	timeout := readFor("copy-1")
	if timeout.Type != "error" || timeout.ID != "copy-1" || timeout.Payload.(map[string]any)["clipboard_operation_timeout"] != true || timeout.Payload.(map[string]any)["native_may_complete_late"] != true {
		t.Fatalf("clipboard timeout response = %+v", timeout)
	}
	if got := writer.calls.Load(); got != 1 {
		t.Fatalf("timed-out write started %d native calls, want 1", got)
	}

	writer.unblock()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		active := server.clipboardActive
		server.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("clipboard slot did not clear after native helper returned")
		}
		time.Sleep(time.Millisecond)
	}
	server.handle(rpcMessage{Version: 1, ID: "copy-3", Type: "clipboard_write", Payload: json.RawMessage(`{"text":"third"}`)}, finished)
	if event := readFor("copy-3"); event.Type != "clipboard_written" || event.ID != "copy-3" {
		t.Fatalf("clipboard did not recover after helper returned: %+v", event)
	}
}

func TestRPCClipboardReadTimeoutKeepsRPCResponsiveAndCorrelated(t *testing.T) {
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	provider := blockingClipboardProvider{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(provider.unblock)
	server := &rpcServer{
		ctx: context.Background(), output: sink, started: true,
		opts: runner.Options{Workspace: t.TempDir()}, requestTypes: make(map[string]string),
		clipboardProvider: &provider, clipboardTimeout: 50 * time.Millisecond,
	}
	server.handle(rpcMessage{Version: 1, ID: "paste-1", Type: "clipboard_paste"}, make(chan turnDone, 1))
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("clipboard provider did not start")
	}
	server.handle(rpcMessage{Version: 1, ID: "status-read", Type: "status"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.ID != "status-read" || event.Type != "status" {
		t.Fatalf("RPC reader blocked during clipboard read: %+v", event)
	}
	var timedOut rpcEvent
	deadline := time.After(time.Second)
	for timedOut.ID != "paste-1" {
		select {
		case raw := <-sink.events:
			if err := json.Unmarshal(raw, &timedOut); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("clipboard read did not time out")
		}
	}
	if timedOut.Type != "error" || timedOut.Payload.(map[string]any)["clipboard_operation_timeout"] != true {
		t.Fatalf("clipboard read timeout=%+v", timedOut)
	}
	server.mu.Lock()
	active := server.clipboardActive
	server.mu.Unlock()
	if !active {
		t.Fatal("slot cleared before non-cancellable provider returned")
	}
	provider.unblock()
	deadline = time.After(time.Second)
	for {
		server.mu.Lock()
		active = server.clipboardActive
		server.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-deadline:
			t.Fatal("clipboard read slot did not clear after provider returned")
		case <-time.After(time.Millisecond):
		}
	}
}
