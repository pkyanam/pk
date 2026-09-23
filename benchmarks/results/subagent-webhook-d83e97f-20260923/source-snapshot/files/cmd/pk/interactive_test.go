package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
)

func TestInteractiveLoopKeepsOneSessionAcrossPromptsAndEOF(t *testing.T) {
	var seenPrompts, seenSessions []string
	var output, diagnostics strings.Builder
	options := runner.Options{Output: &output}
	calls := 0
	runTurn := func(_ context.Context, current runner.Options) (runner.RunResult, error) {
		seenPrompts = append(seenPrompts, current.Prompt)
		seenSessions = append(seenSessions, current.SessionID)
		calls++
		if current.OnSession != nil {
			current.OnSession("session-1")
		}
		fmt.Fprintln(current.Output, "answer")
		return runner.RunResult{SessionID: "session-1", Text: "answer\n"}, nil
	}
	if err := interactiveLoop(t.Context(), strings.NewReader("first prompt\nsecond prompt\n"), &diagnostics, options, runTurn); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.EqualFold(strings.Join(seenPrompts, ","), "first prompt,second prompt") {
		t.Fatalf("calls/prompts = %d / %#v", calls, seenPrompts)
	}
	if len(seenSessions) != 2 || seenSessions[0] != "" || seenSessions[1] != "session-1" {
		t.Fatalf("session IDs passed between turns = %#v", seenSessions)
	}
	if got := strings.Count(diagnostics.String(), "Session: session-1"); got != 1 {
		t.Fatalf("session header appeared %d times, diagnostics: %q", got, diagnostics.String())
	}
	if strings.Count(output.String(), "answer") != 2 {
		t.Fatalf("assistant output = %q", output.String())
	}
}

func TestInteractiveLoopProcessesFinalPromptWithoutNewlineAndCommands(t *testing.T) {
	var diagnostics strings.Builder
	calls := 0
	runTurn := func(_ context.Context, options runner.Options) (runner.RunResult, error) {
		calls++
		if options.Prompt != "last prompt" {
			t.Fatalf("prompt = %q", options.Prompt)
		}
		return runner.RunResult{SessionID: "session"}, nil
	}
	if err := interactiveLoop(t.Context(), strings.NewReader("/help\nlast prompt"), &diagnostics, runner.Options{}, runTurn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(diagnostics.String(), "Commands: /help, /exit, /quit") {
		t.Fatalf("calls=%d diagnostics=%q", calls, diagnostics.String())
	}
	calls = 0
	if err := interactiveLoop(t.Context(), strings.NewReader("/exit\nignored\n"), &diagnostics, runner.Options{}, runTurn); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("exit command ran %d prompts", calls)
	}
}

type promptNotifyingWriter struct {
	mu   sync.Mutex
	text strings.Builder
	seen chan struct{}
}

func (writer *promptNotifyingWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if _, err := writer.text.Write(value); err != nil {
		return 0, err
	}
	select {
	case writer.seen <- struct{}{}:
	default:
	}
	return len(value), nil
}

func TestInteractiveLoopCtrlCCancelsBlockedPromptRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	diagnostics := &promptNotifyingWriter{seen: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() {
		done <- interactiveLoop(ctx, reader, diagnostics, runner.Options{}, func(context.Context, runner.Options) (runner.RunResult, error) {
			t.Error("runner should not be called before a prompt")
			return runner.RunResult{}, nil
		})
	}()
	select {
	case <-diagnostics.seen:
	case <-time.After(time.Second):
		t.Fatal("interactive prompt was not shown")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interactiveLoop() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ctrl-C cancellation did not interrupt blocked stdin read")
	}
}
