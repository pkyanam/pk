//go:build !windows

package extensions

import (
	"context"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestProcessWorkerCancellationKillsDescendants(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	t.Setenv("PK_EXTENSION_PROCESS_HELPER", "spawn-child")
	t.Setenv("PK_EXTENSION_CHILD_PID", pidFile)
	worker := testProcessWorker(t)
	var result string
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err := worker.Call(ctx, "test", struct{}{}, &result)
	cancel()
	if err == nil {
		t.Fatal("expected helper timeout")
	}
	_ = worker.Close()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("extension descendant pid %d remained alive: %v", pid, err)
}
