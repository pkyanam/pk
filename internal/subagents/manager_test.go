package subagents

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/runner"
)

func TestManagerLaunchSteerWaitAndFinalAnswerPhase(t *testing.T) {
	events := make(chan Event, 8)
	m, err := New(Config{Workspace: t.TempDir(), Events: func(e Event) { events <- e }, Runner: func(ctx context.Context, opts runner.Options) (runner.RunResult, error) {
		if opts.Model != "gpt-6-luna" || opts.Effort != "medium" {
			return runner.RunResult{}, fmt.Errorf("unexpected defaults %q/%q", opts.Model, opts.Effort)
		}
		if !opts.JSONL || !opts.ToolEvents || !opts.QueueInputs {
			return runner.RunResult{}, fmt.Errorf("child runner lacks event/steering options")
		}
		if _, err := fmt.Fprintln(opts.Output, `{"type":"session","session_id":"child-session"}`); err != nil {
			return runner.RunResult{}, err
		}
		input := <-opts.Inputs
		if input.Text != "please verify" {
			return runner.RunResult{}, fmt.Errorf("wrong input %q", input.Text)
		}
		input.Accepted(nil)
		if _, err := fmt.Fprintln(opts.Output, `{"type":"assistant","phase":"commentary","text":"I am checking"}`); err != nil {
			return runner.RunResult{}, err
		}
		if _, err := fmt.Fprintln(opts.Output, `{"type":"assistant","phase":"final_answer","text":"Done; tests pass."}`); err != nil {
			return runner.RunResult{}, err
		}
		for range opts.Inputs {
		}
		return runner.RunResult{SessionID: "child-session", Text: "Done; tests pass."}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.Launch(context.Background(), LaunchRequest{Task: "Implement helper", Files: []string{"internal/helper.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SendInput(context.Background(), child.ID, "please verify"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	report, err := m.Wait(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Child.State != "completed" || !strings.Contains(report.Text, "Done; tests pass.") {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Child.SessionID != "child-session" {
		t.Fatalf("session id not recorded: %+v", report.Child)
	}
	var phases []string
	for len(events) > 0 {
		e := <-events
		if e.Type == "assistant" {
			phases = append(phases, e.Phase)
		}
	}
	if len(phases) != 2 || phases[0] != "commentary" || phases[1] != "final_answer" {
		t.Fatalf("assistant event phases = %v", phases)
	}
}

func TestLaunchRejectsNestedAndUnsafeOwnership(t *testing.T) {
	m, err := New(Config{Workspace: t.TempDir(), Depth: 1, Runner: func(context.Context, runner.Options) (runner.RunResult, error) { return runner.RunResult{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Launch(context.Background(), LaunchRequest{Task: "task", Files: []string{"a.go"}}); err == nil {
		t.Fatal("nested launch was accepted")
	}
	m.cfg.Depth = 0
	if _, err = m.Launch(context.Background(), LaunchRequest{Task: "task", Files: []string{"../secret"}}); err == nil {
		t.Fatal("parent traversal ownership was accepted")
	}
}

func TestCloseCancelsAndWaitsForChild(t *testing.T) {
	started := make(chan struct{})
	m, err := New(Config{Workspace: t.TempDir(), Runner: func(ctx context.Context, opts runner.Options) (runner.RunResult, error) {
		close(started)
		<-ctx.Done()
		return runner.RunResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Launch(context.Background(), LaunchRequest{Task: "long task", Files: []string{"owned.go"}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close returned late or failed to wait for the child")
	}
	status, ok := m.Status(c.ID)
	if !ok || status.State != "canceled" {
		t.Fatalf("child status after Close = %+v, found=%v", status, ok)
	}
}
