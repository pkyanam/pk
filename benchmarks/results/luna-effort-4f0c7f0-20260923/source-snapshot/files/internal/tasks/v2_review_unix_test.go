//go:build !windows

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// This test binary doubles as a tiny detached worker. Keeping the helper
// separate from the production CLI makes these lifecycle tests hermetic.
func TestV2ReviewDetachedWorker(t *testing.T) {
	if os.Getenv("PK_TASK_STORE") == "" {
		return
	}
	mode := os.Getenv("PK_TASK_REVIEW_MODE")
	err := RunWorker(context.Background(), os.Getenv("PK_TASK_ID"), func(ctx context.Context, options WorkerOptions, out io.Writer) error {
		if mode == "resume" && !options.Resume {
			options.OnSession("review-session")
		}
		if _, e := io.WriteString(out, "worker-ready\n"); e != nil {
			return e
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		os.Exit(3)
	}
}

func reviewStore(t *testing.T, mode string) Store {
	t.Helper()
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TASK_REVIEW_BINARY", binary)
	t.Setenv("PK_TASK_REVIEW_MODE", mode)
	script := filepath.Join(root, "worker.sh")
	if err = os.WriteFile(script, []byte("#!/bin/sh\nPK_TASK_ID=\"$2\" exec \"$TASK_REVIEW_BINARY\" -test.run=^TestV2ReviewDetachedWorker$\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Store{Root: filepath.Join(root, "tasks")}
}

func reviewWaitTask(t *testing.T, store Store, id string, match func(Task) bool) Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	for {
		task, err := store.Get(id)
		if err == nil && match(task) {
			return task
		}
		select {
		case <-ctx.Done():
			t.Fatalf("task %s did not reach expected state; last task=%+v err=%v", id, task, err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func reviewWaitWorkerExit(t *testing.T, store Store, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := filepath.Join(store.Root, id)
	for {
		busy, release, err := tryTaskLock(dir)
		if err == nil && !busy {
			release()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("worker lock did not release for task %s", id)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestV2ReviewCancelRequestReachesDetachedWorker(t *testing.T) {
	store := reviewStore(t, "cancel")
	task, err := store.Start(context.Background(), StartOptions{ID: "review-cancel", Prompt: "wait for cancellation", Workspace: t.TempDir(), Executable: filepath.Join(filepath.Dir(store.Root), "worker.sh")})
	if err != nil {
		t.Fatal(err)
	}
	reviewWaitTask(t, store, task.ID, func(task Task) bool { return task.Status == StatusRunning })
	if err = store.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	final := reviewWaitTask(t, store, task.ID, func(task Task) bool { return task.Status == StatusCanceled })
	if final.FinishedAt == nil {
		t.Fatalf("canceled task has no terminal timestamp: %+v", final)
	}
	reviewWaitWorkerExit(t, store, task.ID)
}

func TestV2ReviewConcurrentResumeHasSingleWorkerAndCancelWorks(t *testing.T) {
	store := reviewStore(t, "resume")
	task, err := store.Start(context.Background(), StartOptions{ID: "review-resume", Prompt: "preserve session", Workspace: t.TempDir(), Executable: filepath.Join(filepath.Dir(store.Root), "worker.sh")})
	if err != nil {
		t.Fatal(err)
	}
	task = reviewWaitTask(t, store, task.ID, func(task Task) bool { return task.SessionID == "review-session" })
	proc, err := os.FindProcess(task.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = proc.Kill(); err != nil {
		t.Fatal(err)
	}
	reviewWaitTask(t, store, task.ID, func(task Task) bool { return task.Status == StatusInterrupted })

	type result struct {
		task Task
		err  error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(2)
	for range 2 {
		go func() {
			start.Done()
			resumed, e := store.Resume(context.Background(), task.ID)
			results <- result{task: resumed, err: e}
		}()
	}
	start.Wait()
	first, second := <-results, <-results
	successes := 0
	for _, got := range []result{first, second} {
		if got.err == nil {
			successes++
		} else if !errors.Is(got.err, ErrAlreadyRunning) {
			t.Fatalf("concurrent Resume returned unexpected error: %v", got.err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent Resume successes=%d, want exactly one: %v / %v", successes, first.err, second.err)
	}
	if err = store.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	reviewWaitTask(t, store, task.ID, func(task Task) bool { return task.Status == StatusCanceled })
	reviewWaitWorkerExit(t, store, task.ID)
}

func TestV2ReviewAppendEventRepairsTornTailAndHandlesLargeRecord(t *testing.T) {
	dir := t.TempDir()
	task := Task{ID: "review-events", Status: StatusRunning}
	if err := writeTask(dir, task); err != nil {
		t.Fatal(err)
	}
	first, err := appendEvent(dir, &task, Event{Type: "started"})
	if err != nil || first.Seq != 1 {
		t.Fatalf("append initial event = %+v, %v", first, err)
	}
	// Simulate a crash during the next JSONL write; the subsequent append must
	// discard only this incomplete suffix and continue sequence numbers.
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(`{"seq":2,"type":"partial`)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write simulated torn tail: write=%v close=%v", writeErr, closeErr)
	}
	large, err := appendEvent(dir, &task, Event{Type: "output", Text: string(make([]byte, 129*1024))})
	if err != nil {
		t.Fatalf("append large valid event after torn tail: %v", err)
	}
	last, err := appendEvent(dir, &task, Event{Type: "succeeded"})
	if err != nil {
		t.Fatalf("append after large event: %v", err)
	}
	if large.Seq != 2 || last.Seq != 3 {
		t.Fatalf("sequence after torn tail/large record = %d, %d; want 2, 3", large.Seq, last.Seq)
	}
	offset := int64(0)
	events, more, err := readEventsFrom(dir, 0, &offset)
	if err != nil {
		t.Fatal(err)
	}
	if more || len(events) != 3 || events[0].Seq != 1 || events[1].Seq != 2 || events[2].Seq != 3 {
		t.Fatalf("recovered event stream: len=%d more=%t events=%+v", len(events), more, events)
	}
	// Ensure the written stream remains valid JSONL, not merely readable by a
	// code path that silently skipped malformed records.
	for i, event := range events {
		if _, err = json.Marshal(event); err != nil {
			t.Fatalf("event %d cannot be marshaled after recovery: %v", i, err)
		}
	}
}
