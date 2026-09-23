package tasks

import (
	"testing"
	"time"
)

// This reproduces the lost-update ordering from worker shutdown: an older
// task snapshot exists, an output event advances LastEvent, then the worker
// tries to publish its terminal state. Finalization must reload under the
// event lock rather than writing that stale snapshot back over the new cursor.
func TestFinishTaskPreservesEventSequenceAfterConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	started := Task{ID: "sequence-check", Status: StatusRunning, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := writeTask(dir, started); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(dir, &started, Event{Type: "started"}); err != nil {
		t.Fatal(err)
	}
	staleSnapshot, err := readTask(dir)
	if err != nil {
		t.Fatal(err)
	}
	output, err := appendEvent(dir, &started, Event{Type: "output", Text: "last output"})
	if err != nil {
		t.Fatal(err)
	}
	if output.Seq <= staleSnapshot.LastEvent {
		t.Fatalf("setup failed to advance sequence: stale=%d output=%d", staleSnapshot.LastEvent, output.Seq)
	}

	if err := finishTask(dir, StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	finished, err := readTask(dir)
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := readEventsFrom(dir, 0, new(int64))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1].Type != string(StatusSucceeded) {
		t.Fatalf("terminal event missing or not last: %+v", events)
	}
	terminalSeq := events[len(events)-1].Seq
	if finished.Status != StatusSucceeded || finished.LastEvent != terminalSeq || terminalSeq <= output.Seq {
		t.Fatalf("finished status/cursor=%s/%d, output seq=%d, terminal seq=%d", finished.Status, finished.LastEvent, output.Seq, terminalSeq)
	}
	if staleSnapshot.LastEvent >= finished.LastEvent {
		t.Fatalf("terminal finalization regressed to stale cursor: stale=%d finished=%d", staleSnapshot.LastEvent, finished.LastEvent)
	}
}
