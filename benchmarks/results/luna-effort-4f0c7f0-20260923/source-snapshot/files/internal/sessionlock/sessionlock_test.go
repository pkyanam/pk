package sessionlock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseExcludesConcurrentAccessAndReleases(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir, "session-a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lease error = %v, want ErrBusy", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(dir, "session-a")
	if err != nil {
		t.Fatalf("lease after release: %v", err)
	}
	_ = second.Release()
}

func TestLeaseExcludesOtherProcess(t *testing.T) {
	if os.Getenv("PK_SESSION_LOCK_HELPER") == "1" {
		dir := os.Getenv("PK_SESSION_LOCK_DIR")
		lease, err := Acquire(dir, "session-cross-process")
		if err != nil {
			t.Fatalf("child acquire: %v", err)
		}
		defer lease.Release()
		if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseExcludesOtherProcess$")
	cmd.Env = append(os.Environ(), "PK_SESSION_LOCK_HELPER=1", "PK_SESSION_LOCK_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(dir, "stop"), []byte("stop"), 0o600)
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not acquire the session lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if busy, err := IsBusy(dir, "session-cross-process"); err != nil || !busy {
		t.Fatalf("cross-process lease busy=%v err=%v", busy, err)
	}
}

func TestAcquireRejectsUnsafeID(t *testing.T) {
	if _, err := Acquire(t.TempDir(), "../outside"); err == nil {
		t.Fatal("unsafe ID accepted")
	}
}
