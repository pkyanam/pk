//go:build darwin || linux

package attachments

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLoadRejectsFIFOWithoutOpeningIt(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Load(context.Background(), workspace, []string{"pipe"}, Limits{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO load error = %v", err)
		}
	case <-time.After(time.Second):
		_ = os.Remove(path)
		t.Fatal("Load blocked while opening a named pipe")
	}
}
