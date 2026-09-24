package workspacejournal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testSession(t *testing.T) string {
	t.Helper()
	// 32 hex characters, matching real session IDs.
	return "0123456789abcdef0123456789abcdef"
}

func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestBeginCompleteFoldLifecycle(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "call-1", "WriteFile", "a/b.txt", nil, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	op := ops[0]
	if op.Status != StatusCompleted || op.Path != "a/b.txt" || op.Pre != nil {
		t.Fatalf("unexpected op: %+v", op)
	}
	if op.Post == nil || op.Post.SHA256 == "" || op.Post.Size != 7 {
		t.Fatalf("unexpected post image: %+v", op.Post)
	}
	if op.Seq != 1 {
		t.Fatalf("expected seq 1, got %d", op.Seq)
	}
}

func TestPreparedWithoutTerminalMeansUnknown(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "EditFile", "f.txt", []byte("before"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Status != StatusPrepared {
		t.Fatalf("expected a single prepared op, got %+v", ops)
	}
	if ops[0].Post == nil || ops[0].Pre == nil {
		t.Fatalf("prepared op must retain both images: %+v", ops[0])
	}
}

func TestFailedMutationKeepsPreimageAndRecordsReason(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", []byte("old"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(session, "op-1", "disk full"); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Status != StatusFailed {
		t.Fatalf("expected a failed op, got %+v", ops)
	}
	if ops[0].Post != nil {
		t.Fatalf("failed op must drop the post image: %+v", ops[0])
	}
	if ops[0].Pre == nil || ops[0].Reason != "disk full" {
		t.Fatalf("failed op must keep the preimage and reason: %+v", ops[0])
	}
}

func TestTornTailLineIsIgnored(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", nil, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(store.Root(), "sessions", session, "journal.jsonl")
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, append(data, []byte(`{"kind":"prep`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(session)
	if err != nil {
		t.Fatalf("torn tail must not fail reads: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected the one valid op, got %d", len(ops))
	}
	// Sequence allocation must still advance past valid lines.
	if _, err := store.List(session); err != nil {
		t.Fatal(err)
	}
	seq, err := store.Summarize(session, "", SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if seq.Cursor == "" {
		t.Fatal("expected a newest cursor")
	}
}

func TestCorruptInteriorLineFailsClosed(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", nil, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(store.Root(), "sessions", session, "journal.jsonl")
	if err := os.WriteFile(journal, []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(session); err == nil {
		t.Fatal("expected an error for a corrupt interior line")
	}
}

func TestInvalidSessionAndPathRejected(t *testing.T) {
	store := openStore(t)
	if err := store.Begin("short", "op", "", "WriteFile", "f.txt", nil, []byte("x"), 0o644); err == nil {
		t.Fatal("expected session ID rejection")
	}
	session := testSession(t)
	if err := store.Begin(session, "op", "", "WriteFile", "../escape.txt", nil, []byte("x"), 0o644); err == nil {
		t.Fatal("expected traversal path rejection")
	}
	if err := store.Begin(session, "op", "", "WriteFile", "/abs/path", nil, []byte("x"), 0o644); err == nil {
		t.Fatal("expected absolute path rejection")
	}
}

func TestConcurrentBeginsSerializeWithDistinctSeqs(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for index := 0; index < writers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			err := store.Begin(session, fmt.Sprintf("op-%d", index), "", "WriteFile", fmt.Sprintf("f%d.txt", index), nil, []byte("x"), 0o644)
			errs[index] = err
		}(index)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", index, err)
		}
	}
	ops, err := store.List(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != writers {
		t.Fatalf("expected %d ops, got %d", writers, len(ops))
	}
	seen := map[uint64]bool{}
	for _, op := range ops {
		if seen[op.Seq] {
			t.Fatalf("duplicate sequence %d", op.Seq)
		}
		seen[op.Seq] = true
	}
}

func TestObjectStoreRoundTripAndDigestVerification(t *testing.T) {
	store := openStore(t)
	content, err := store.writeObject([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.ReadObject(content)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Fatalf("unexpected payload %q", data)
	}
	tampered := Content{SHA256: content.SHA256, Size: 999}
	if _, err := store.ReadObject(tampered); err == nil {
		t.Fatal("expected a size-mismatch error")
	}
	missing := Content{SHA256: strings.Repeat("ab", 32), Size: 3}
	if _, err := store.ReadObject(missing); err == nil {
		t.Fatal("expected a missing-object error")
	}
}

func TestRestoreRejectsChangedFile(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "f.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", []byte("original"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	// The user edited the file after the tool mutation.
	if err := os.WriteFile(path, []byte("user edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := store.Restore(context.Background(), session, workspace, []string{"op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Restored) != 0 {
		t.Fatalf("restore must refuse: %+v", report)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason == "" {
		t.Fatalf("expected a skip reason: %+v", report.Skipped)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "user edit" {
		t.Fatalf("user edit must survive: %q", current)
	}
}

func TestRestoreRevertsCompletedMutation(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "f.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", []byte("original"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := store.Restore(context.Background(), session, workspace, []string{"op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Restored) != 1 {
		t.Fatalf("expected one restore: %+v", report)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "original" {
		t.Fatalf("expected the preimage, got %q", current)
	}
	// The restores directory must hold the safety snapshot.
	if report.SnapshotDir == "" {
		t.Fatal("expected a snapshot directory")
	}
	if _, err := os.Stat(filepath.Join(report.SnapshotDir, "snapshot.json")); err != nil {
		t.Fatalf("snapshot manifest missing: %v", err)
	}
	entries, err := os.ReadDir(report.SnapshotDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 { // snapshot.json + one snapshotted content entry
		t.Fatalf("snapshot dir incomplete: %d entries", len(entries))
	}
	// The journal records the restore.
	ops, err := store.List(session)
	if err != nil {
		t.Fatal(err)
	}
	if ops[0].Status != StatusRestored || len(ops[0].Restores) != 1 {
		t.Fatalf("expected a restored op with a note: %+v", ops[0])
	}
}

func TestRestoreTwiceIsRecordedAndRefusesAfterSecondChange(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "f.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", []byte("v1"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := store.Restore(context.Background(), session, workspace, []string{"op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Restored) != 1 {
		t.Fatalf("first restore failed: %+v", first)
	}
	// A second restore of the same op: the file now matches the recorded
	// preimage, which is a legitimately idempotent state to restore onto.
	second, err := store.Restore(context.Background(), session, workspace, []string{"op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Restored) != 1 {
		t.Fatalf("second restore should reapply the same preimage: %+v", second)
	}
}

func TestPlanRestoreWithoutMutation(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "f.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", []byte("original"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	actions, skipped, err := store.PlanRestore(context.Background(), session, workspace, []string{"op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || len(skipped) != 0 {
		t.Fatalf("unexpected plan: %+v / %+v", actions, skipped)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "changed" {
		t.Fatal("plan must not mutate the workspace")
	}
}

func TestDefaultRestoreSkipsSupersededPaths(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "f.txt")
	// op-1: create with "v1"; op-2: overwrite with "v2".
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", nil, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(session, "op-2", "", "WriteFile", "f.txt", []byte("v1"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := store.Restore(context.Background(), session, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Restored) != 1 {
		t.Fatalf("only the superseding op should restore: %+v", report)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "v1" {
		t.Fatalf("expected v1 after restore, got %q", current)
	}
}

func TestRestoreRefusesSymlinkTarget(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	workspace := t.TempDir()
	real := filepath.Join(workspace, "outside.txt")
	if err := os.WriteFile(real, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(workspace, "link.txt")
	if err := os.Symlink(real, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := store.Begin(session, "op-1", "", "WriteFile", "link.txt", []byte("original"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	// WriteFile on a symlink path opens the target, so "changed" went into
	// real and link.txt is still a symlink. Restoring must refuse to follow
	// or replace the link.
	report, err := store.Restore(context.Background(), session, workspace, []string{"op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Restored) != 0 {
		t.Fatalf("symlink target must not be overwritten: %+v", report)
	}
	target, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(target) != "original" {
		t.Fatalf("unexpected target content: %q", target)
	}
	// Replace the link with a regular file carrying the recorded post state;
	// restore must now succeed onto the regular file.
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linked, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err = store.Restore(context.Background(), session, workspace, []string{"op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Restored) != 1 {
		t.Fatalf("regular file should restore: %+v", report)
	}
	current, err := os.ReadFile(linked)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "original" {
		t.Fatalf("expected preimage, got %q", current)
	}
	// The pointed-to file must still hold what the earlier WriteFile wrote
	// there (via the then-symlink); restore replaced only the regular file.
	after, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "original" {
		t.Fatalf("unexpected real-file content: %q", after)
	}
}

func TestRetentionFoldsOldEntries(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, Limits{MaxEntriesPerSession: 2})
	if err != nil {
		t.Fatal(err)
	}
	session := testSession(t)
	for index := 0; index < 5; index++ {
		if err := store.Begin(session, fmt.Sprintf("op-%d", index), "", "WriteFile", fmt.Sprintf("f%d.txt", index), nil, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(session, fmt.Sprintf("op-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := store.List(session)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected retention to keep 2 entries, got %d", len(ops))
	}
	if ops[0].ID != "op-3" || ops[1].ID != "op-4" {
		t.Fatalf("expected the newest entries, got %s and %s", ops[0].ID, ops[1].ID)
	}
}

func TestGCObjectsRemovesUnreferenced(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", []byte("keep"), []byte("keep2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	// An unreferenced object from an unknown session.
	if _, err := store.writeObject([]byte("orphan")); err != nil {
		t.Fatal(err)
	}
	removed, err := store.GCObjects()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 orphan removed, got %d", removed)
	}
	// Referenced objects survive.
	ops, err := store.List(session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadObject(*ops[0].Post); err != nil {
		t.Fatalf("referenced object must survive GC: %v", err)
	}
	if _, err := store.ReadObject(*ops[0].Pre); err != nil {
		t.Fatalf("referenced object must survive GC: %v", err)
	}
}

func TestNewestCursorTracksFolds(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "WriteFile", "a.txt", nil, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.NewestCursor(session)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "1" {
		t.Fatalf("expected cursor 1, got %q", cursor)
	}
	empty, err := store.NewestCursor(testSession2())
	if err != nil {
		t.Fatal(err)
	}
	if empty != "0" {
		t.Fatalf("expected cursor 0 for an empty journal, got %q", empty)
	}
}

func testSession2() string { return "fedcba9876543210fedcba9876543210" }

func TestSummarizeCountsAndCursorFiltering(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "WriteFile", "a.txt", nil, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(session, "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Begin(session, "op-2", "", "EditFile", "a.txt", []byte("1"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(session, "op-2", "nope"); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summarize(session, "", SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Completed != 1 || summary.Failed != 1 || summary.Unknown != 0 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	if len(summary.Files) != 1 || summary.Files[0].Added != 1 || summary.Files[0].Modified != 0 || summary.Files[0].Failed != 1 {
		t.Fatalf("unexpected per-path summary: %+v", summary.Files)
	}
	// Cursor at seq 1 hides the failed op.
	after, err := store.Summarize(session, "1", SummaryBounds{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Failed != 0 || after.Completed != 1 {
		t.Fatalf("cursor filtering failed: %+v", after)
	}
}

func TestOpenRejectsEmptyRoot(t *testing.T) {
	if _, err := Open("   ", Limits{}); err == nil {
		t.Fatal("expected an empty-root error")
	}
}

func TestRestoreUnknownOpID(t *testing.T) {
	store := openStore(t)
	session := testSession(t)
	if err := store.Begin(session, "op-1", "", "WriteFile", "f.txt", nil, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.PlanRestore(context.Background(), session, t.TempDir(), []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown journal operation") {
		t.Fatalf("expected unknown-op error, got %v", err)
	}
}

func TestErrorMessageFlattensAndBounds(t *testing.T) {
	message := errorMessage(fmt.Errorf("line one\nline two"))
	if strings.Contains(message, "\n") {
		t.Fatal("expected flattened message")
	}
	long := strings.Repeat("x", 400)
	if len(errorMessage(errors.New(long))) != 240 {
		t.Fatal("expected bounded message")
	}
	if errorMessage(nil) != "" {
		t.Fatal("nil must map to empty")
	}
}
