//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package filetools

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadTextRejectsFIFOWithoutBlocking(t *testing.T) {
	workspace := t.TempDir()
	pipe := filepath.Join(workspace, "pipe")
	if err := unix.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := readText(context.Background(), workspace, "pipe", 1, 1); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO read unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Read blocked opening a FIFO")
	}
}
