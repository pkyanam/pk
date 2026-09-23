package subagents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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

func TestTaskOnlyModeNeedsNoDummyFilesAndStatesPermissionBoundary(t *testing.T) {
	started := make(chan string, 1)
	m, err := New(Config{Workspace: t.TempDir(), Runner: func(_ context.Context, opts runner.Options) (runner.RunResult, error) {
		started <- opts.Prompt
		return runner.RunResult{Text: "investigation complete"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.Launch(context.Background(), LaunchRequest{Task: "Investigate the issue", TaskOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !child.TaskOnly || len(child.Files) != 0 {
		t.Fatalf("task-only child=%+v", child)
	}
	select {
	case prompt := <-started:
		if !strings.Contains(prompt, "not an OS sandbox or read-only mode") || !strings.Contains(prompt, "no files are assigned") {
			t.Fatalf("task-only prompt overstates restrictions or omits scope: %q", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("task-only child did not start")
	}
	if _, err := m.Launch(context.Background(), LaunchRequest{Task: "Missing scope"}); err == nil {
		t.Fatal("launch without files or explicit task-only mode was accepted")
	}
	if _, err := m.Launch(context.Background(), LaunchRequest{Task: "Conflicting modes", TaskOnly: true, Files: []string{"a.go"}}); err == nil {
		t.Fatal("task-only launch accepted file ownership declarations")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.Wait(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	m.Close()
}

func TestQueuedDispatchIsBoundedFIFOAndCancellationReleasesClaims(t *testing.T) {
	started := make(chan string, 16)
	m, err := New(Config{Workspace: t.TempDir(), MaxConcurrent: 1, Runner: func(ctx context.Context, opts runner.Options) (runner.RunResult, error) {
		started <- opts.Prompt
		<-ctx.Done()
		return runner.RunResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	launch := func(task, file string) Child {
		t.Helper()
		child, err := m.Launch(context.Background(), LaunchRequest{Task: task, Files: []string{file}})
		if err != nil {
			t.Fatal(err)
		}
		return child
	}
	first := launch("first", "owned/first.go")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first child did not start")
	}
	queued := make([]Child, 0, maxQueuedChildren)
	for index := 0; index < maxQueuedChildren; index++ {
		queued = append(queued, launch(fmt.Sprintf("queued-%d", index), fmt.Sprintf("owned/q%d.go", index)))
	}
	if _, err := m.Launch(context.Background(), LaunchRequest{Task: "over queue bound", TaskOnly: true}); err == nil {
		t.Fatal("launch beyond bounded queue was accepted")
	}
	if status, _ := m.Status(queued[0].ID); status.State != "queued" {
		t.Fatalf("queued state=%q", status.State)
	}
	if _, err := m.Launch(context.Background(), LaunchRequest{Task: "overlap queued owner", Files: []string{"owned/q2.go/child.go"}}); err == nil {
		t.Fatal("ownership overlap with a queued child was accepted")
	}
	if err := m.Cancel(queued[2].ID); err != nil {
		t.Fatal(err)
	}
	if report, err := m.Wait(context.Background(), queued[2].ID); err != nil || report.Child.State != "canceled" {
		t.Fatalf("canceled queued report=%+v err=%v", report, err)
	}
	// Canceling a queued owner immediately releases its claim, including while
	// other children still wait for the active slot.
	replacement := launch("replacement", "owned/q2.go")
	if status, _ := m.Status(replacement.ID); status.State != "queued" {
		t.Fatalf("replacement state=%q", status.State)
	}
	if err := m.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.Wait(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case prompt := <-started:
		if !strings.Contains(prompt, "queued-0") {
			t.Fatalf("queue did not start FIFO: %q", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("queued child did not receive released slot")
	}
	m.Close()
	for _, child := range append(queued, replacement) {
		status, ok := m.Status(child.ID)
		if !ok || status.State == "queued" || status.State == "running" {
			t.Fatalf("child did not settle after close: %+v found=%v", status, ok)
		}
	}
}

func TestConcurrentLaunchAndCancelNeverExceedsActiveLimit(t *testing.T) {
	var active, peak atomic.Int32
	m, err := New(Config{Workspace: t.TempDir(), MaxConcurrent: 2, Runner: func(ctx context.Context, _ runner.Options) (runner.RunResult, error) {
		current := active.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		<-ctx.Done()
		active.Add(-1)
		return runner.RunResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 48)
	for index := 0; index < 48; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			child, err := m.Launch(context.Background(), LaunchRequest{Task: fmt.Sprintf("stress-%d", index), TaskOnly: true})
			if err == nil {
				ids <- child.ID
				_ = m.Cancel(child.ID)
			}
		}(index)
	}
	wg.Wait()
	m.Close()
	if peak.Load() > 2 {
		t.Fatalf("runner factory peak concurrency=%d", peak.Load())
	}
	close(ids)
	for id := range ids {
		child, ok := m.Status(id)
		if !ok || child.State == "running" || child.State == "queued" {
			t.Fatalf("child remained active after close: %+v", child)
		}
	}
}

func TestEventSequenceMatchesCallbackDeliveryOrder(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	delivered := make(chan Event, 2)
	m, err := New(Config{Workspace: t.TempDir(), Events: func(e Event) {
		if e.Text == "first" {
			close(entered)
			<-release
		}
		delivered <- e
	}, Runner: func(context.Context, runner.Options) (runner.RunResult, error) { return runner.RunResult{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	doneFirst := make(chan struct{})
	go func() { m.emit(Event{Type: "test", ChildID: "one", Text: "first"}); close(doneFirst) }()
	<-entered
	doneSecond := make(chan struct{})
	go func() { m.emit(Event{Type: "test", ChildID: "two", Text: "second"}); close(doneSecond) }()
	<-doneSecond // enqueue may return; delivery remains serialized by dispatcher.
	close(release)
	<-doneFirst
	<-doneSecond
	first, second := <-delivered, <-delivered
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("callback sequence order = %d, %d", first.Sequence, second.Sequence)
	}
	m.Close()
}

func TestEventCallbackCanReenterWithoutLockingManagerState(t *testing.T) {
	var m *Manager
	delivered := make(chan Event, 2)
	var err error
	m, err = New(Config{Workspace: t.TempDir(), Events: func(e Event) {
		delivered <- e
		if e.Text == "outer" {
			m.emit(Event{Type: "test", ChildID: "two", Text: "inner"})
			// This state read would deadlock if the callback held the manager
			// state lock, and nested emit must enqueue rather than recurse.
			_, _ = m.Status("missing")
		}
	}, Runner: func(context.Context, runner.Options) (runner.RunResult, error) { return runner.RunResult{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.emit(Event{Type: "test", ChildID: "one", Text: "outer"}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant event callback deadlocked")
	}
	first, second := <-delivered, <-delivered
	if first.Text != "outer" || second.Text != "inner" || first.Sequence >= second.Sequence {
		t.Fatalf("reentrant event order = %+v then %+v", first, second)
	}
	m.Close()
}

func TestCloseWaitsForQueuedEventDelivery(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	delivered := make(chan string, 2)
	m, err := New(Config{Workspace: t.TempDir(), Events: func(e Event) {
		if e.Text == "slow" {
			close(entered)
			<-release
		}
		delivered <- e.Text
	}, Runner: func(context.Context, runner.Options) (runner.RunResult, error) { return runner.RunResult{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	go m.emit(Event{Type: "test", ChildID: "one", Text: "slow"})
	<-entered
	queued := make(chan struct{})
	go func() { m.emit(Event{Type: "test", ChildID: "two", Text: "queued"}); close(queued) }()
	<-queued
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned before the queued event callback finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after event delivery drained")
	}
	if first, second := <-delivered, <-delivered; first != "slow" || second != "queued" {
		t.Fatalf("delivered callbacks = %q then %q", first, second)
	}
}

func TestCancelThenCloseDrainsChildOutputAndTerminalEvent(t *testing.T) {
	enteredCallback := make(chan Event, 1)
	releaseCallback := make(chan struct{})
	delivered := make(chan Event, 8)
	m, err := New(Config{Workspace: t.TempDir(), Events: func(e Event) {
		if e.Type == "assistant" {
			enteredCallback <- e
			<-releaseCallback
		}
		delivered <- e
	}, Runner: func(ctx context.Context, opts runner.Options) (runner.RunResult, error) {
		if _, err := fmt.Fprintln(opts.Output, `{"type":"assistant","phase":"commentary","text":"child progress"}`); err != nil {
			return runner.RunResult{}, err
		}
		<-ctx.Done()
		return runner.RunResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.Launch(context.Background(), LaunchRequest{Task: "long task", Files: []string{"owned.go"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-enteredCallback:
	case <-time.After(time.Second):
		t.Fatal("child progress event was not delivered")
	}
	if err := m.Cancel(child.ID); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while child event delivery was blocked")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseCallback)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after event delivery resumed")
	}
	var events []Event
	for {
		select {
		case e := <-delivered:
			events = append(events, e)
		default:
			goto drained
		}
	}
drained:
	var previous uint64
	terminalCanceled := false
	for _, e := range events {
		if e.Sequence <= previous {
			t.Fatalf("event sequence not increasing: previous=%d event=%+v", previous, e)
		}
		previous = e.Sequence
		if e.Type == "subagent" && e.ChildID == child.ID && e.State == "canceled" {
			terminalCanceled = true
		}
	}
	if !terminalCanceled {
		t.Fatalf("terminal cancellation event was not drained before Close returned: %+v", events)
	}
	status, ok := m.Status(child.ID)
	if !ok || status.State != "canceled" {
		t.Fatalf("child status after cancellation = %+v, found=%v", status, ok)
	}
}

func TestConcurrentCloseWaitsForFirstCloseToDrain(t *testing.T) {
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCallback) }) }
	defer release()
	m, err := New(Config{Workspace: t.TempDir(), Events: func(e Event) {
		if e.Type == "assistant" {
			select {
			case <-callbackEntered:
			default:
				close(callbackEntered)
			}
			<-releaseCallback
		}
	}, Runner: func(ctx context.Context, opts runner.Options) (runner.RunResult, error) {
		if _, err := fmt.Fprintln(opts.Output, `{"type":"assistant","phase":"commentary","text":"blocked callback"}`); err != nil {
			return runner.RunResult{}, err
		}
		<-ctx.Done()
		return runner.RunResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Launch(context.Background(), LaunchRequest{Task: "long task", Files: []string{"owned.go"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-callbackEntered:
	case <-time.After(time.Second):
		t.Fatal("child callback did not block")
	}
	firstDone := make(chan struct{})
	go func() { m.Close(); close(firstDone) }()
	select {
	case <-m.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("first Close did not begin cancellation")
	}
	select {
	case <-firstDone:
		t.Fatal("first Close returned before child output drained")
	case <-time.After(20 * time.Millisecond):
	}
	secondDone := make(chan struct{})
	go func() { m.Close(); close(secondDone) }()
	select {
	case <-secondDone:
		t.Fatal("concurrent Close returned before the first Close finished draining")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	for _, done := range []<-chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Close did not finish after child event delivery resumed")
		}
	}
}

func TestOwnershipClaimsResolveSymlinkAliasesAndMacCaseAliases(t *testing.T) {
	workspace := t.TempDir()
	actual := filepath.Join(workspace, "Actual.go")
	if err := os.WriteFile(actual, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "alias.go")
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	m, err := New(Config{Workspace: workspace, MaxConcurrent: 1, Runner: func(ctx context.Context, _ runner.Options) (runner.RunResult, error) {
		<-ctx.Done()
		return runner.RunResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Launch(context.Background(), LaunchRequest{Task: "first", Files: []string{"Actual.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Launch(context.Background(), LaunchRequest{Task: "symlink alias", Files: []string{"alias.go"}}); err == nil {
		t.Fatal("symlink alias did not conflict with active claim")
	}
	if runtime.GOOS == "darwin" {
		if _, err := m.Launch(context.Background(), LaunchRequest{Task: "case alias", Files: []string{"actual.go"}}); err == nil {
			t.Fatal("case alias did not conflict on macOS")
		}
	}
	if err := m.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	m.Close()
}
