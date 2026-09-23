package jobqueue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func testQueue(t *testing.T) (*Queue, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	queue, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	return queue, store
}

func enqueueAndClaim(t *testing.T, queue *Queue, now time.Time, id string, maxAttempts int) Job {
	t.Helper()
	if _, err := queue.Enqueue(context.Background(), Job{ID: id, Payload: []byte("payload:" + id), MaxAttempts: maxAttempts}); err != nil {
		t.Fatal(err)
	}
	job, ok, err := queue.Claim(context.Background(), now, time.Minute)
	if err != nil || !ok || job.ID != id {
		t.Fatalf("claim = %+v, ok=%t, err=%v", job, ok, err)
	}
	return job
}

func TestRecoverExpiredRequeuesAndCanBeClaimedAgain(t *testing.T) {
	queue, _ := testQueue(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := enqueueAndClaim(t, queue, now, "retryable", 3)
	if first.Attempts != 1 || first.State != StateLeased {
		t.Fatalf("first claim = %+v", first)
	}
	recovered, err := queue.RecoverExpired(context.Background(), first.LeaseUntil)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovery = %+v, err=%v", recovered, err)
	}
	job := recovered[0]
	if job.State != StateReady || job.Attempts != 1 || !job.LeaseUntil.IsZero() {
		t.Fatalf("recovered job = %+v", job)
	}
	again, ok, err := queue.Claim(context.Background(), first.LeaseUntil, time.Minute)
	if err != nil || !ok || again.Attempts != 2 || again.State != StateLeased {
		t.Fatalf("reclaim = %+v, ok=%t, err=%v", again, ok, err)
	}
}

func TestRecoverExpiredMarksExhaustedJobsDead(t *testing.T) {
	queue, _ := testQueue(t)
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	first := enqueueAndClaim(t, queue, now, "exhausted", 1)
	recovered, err := queue.RecoverExpired(context.Background(), first.LeaseUntil)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovery = %+v, err=%v", recovered, err)
	}
	if recovered[0].State != StateDead || recovered[0].Attempts != 1 || !recovered[0].LeaseUntil.IsZero() {
		t.Fatalf("exhausted job = %+v", recovered[0])
	}
}

func TestRecoverExpiredUsesStrictExpiryAndLeavesTerminalJobsAlone(t *testing.T) {
	queue, _ := testQueue(t)
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	leased := enqueueAndClaim(t, queue, now, "live", 2)
	if _, err := queue.Enqueue(context.Background(), Job{ID: "done", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	doneLease, ok, err := queue.Claim(context.Background(), now, time.Minute)
	if err != nil || !ok || doneLease.ID != "done" {
		t.Fatalf("claim terminal-job fixture = %+v, ok=%t, err=%v", doneLease, ok, err)
	}
	if _, err := queue.Complete(context.Background(), doneLease.ID, now); err != nil {
		t.Fatal(err)
	}
	before, err := queue.RecoverExpired(context.Background(), leased.LeaseUntil.Add(-time.Nanosecond))
	if err != nil || len(before) != 0 {
		t.Fatalf("early recovery = %+v, err=%v", before, err)
	}
	atExpiry, err := queue.RecoverExpired(context.Background(), leased.LeaseUntil)
	if err != nil || len(atExpiry) != 1 || atExpiry[0].ID != "live" {
		t.Fatalf("boundary recovery = %+v, err=%v", atExpiry, err)
	}
}

func TestRecoverExpiredIsIdempotentAndPreservesFIFOOrder(t *testing.T) {
	queue, _ := testQueue(t)
	now := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	var expiry time.Time
	for _, id := range []string{"first", "second", "third"} {
		job := enqueueAndClaim(t, queue, now, id, 2)
		expiry = job.LeaseUntil
	}
	first, err := queue.RecoverExpired(context.Background(), expiry)
	if err != nil || len(first) != 3 {
		t.Fatalf("first recovery = %+v, err=%v", first, err)
	}
	for i, id := range []string{"first", "second", "third"} {
		if first[i].ID != id {
			t.Fatalf("recovery order = %+v", first)
		}
	}
	second, err := queue.RecoverExpired(context.Background(), expiry)
	if err != nil || len(second) != 0 {
		t.Fatalf("second recovery = %+v, err=%v", second, err)
	}
}

func TestConcurrentRecoveryTransitionsEachExpiredJobOnce(t *testing.T) {
	queue, _ := testQueue(t)
	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	lease := enqueueAndClaim(t, queue, now, "shared", 3)
	const workers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	recoveredCount := 0
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			jobs, err := queue.RecoverExpired(context.Background(), lease.LeaseUntil)
			if err != nil {
				t.Errorf("recovery: %v", err)
				return
			}
			mu.Lock()
			recoveredCount += len(jobs)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if recoveredCount != 1 {
		t.Fatalf("concurrent recovery returned %d transitions, want 1", recoveredCount)
	}
}

func TestRecoverExpiredHonorsCanceledContextAndReturnsDetachedPayload(t *testing.T) {
	queue, store := testQueue(t)
	now := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	lease := enqueueAndClaim(t, queue, now, "payload", 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queue.RecoverExpired(ctx, lease.LeaseUntil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery error = %v", err)
	}
	jobs, err := queue.RecoverExpired(context.Background(), lease.LeaseUntil)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("recovery = %+v, err=%v", jobs, err)
	}
	jobs[0].Payload[0] = 'X'
	stored, err := store.Get(context.Background(), "payload")
	if err != nil || string(stored.Payload) != "payload:payload" {
		t.Fatalf("caller mutated stored payload: %+v, err=%v", stored, err)
	}
}
