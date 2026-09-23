//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestRunCommandContextTerminatesProcessGroupOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	configureProcessGroup(cmd)
	started := time.Now()
	err := runCommandContext(ctx, cmd)
	if err == nil {
		t.Fatal("canceled shell command unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("process group did not terminate promptly: %s", elapsed)
	}
}
