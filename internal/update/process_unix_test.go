//go:build !windows

package update

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelledBuildStopsDescendantHoldingPipes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- (osCommandRunner{}).Run(ctx, t.TempDir(), "sh", &out, &out, "-c", `(sleep 1; echo alive > "$1") & wait`, "sh", marker)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled build succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("build retained descendant pipes after cancellation")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled descendant wrote marker: %v", err)
	}
}
