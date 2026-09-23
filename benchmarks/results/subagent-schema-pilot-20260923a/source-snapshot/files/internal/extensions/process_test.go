package extensions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExtensionProcessHelper(t *testing.T) {
	mode := os.Getenv("PK_EXTENSION_PROCESS_HELPER")
	if mode == "" {
		return
	}
	if mode == "crash" {
		os.Exit(17)
	}
	if mode == "hang" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "spawn-child" {
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			os.Exit(19)
		}
		_ = os.WriteFile(os.Getenv("PK_EXTENSION_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		for {
			time.Sleep(time.Hour)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			os.Exit(18)
		}
		if mode == "lifecycle-slow" && req.Method == "lifecycle.notify" {
			_ = os.WriteFile(os.Getenv("PK_EXTENSION_LIFECYCLE_SEEN"), []byte("seen"), 0o600)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if mode == "wrong-id" {
			_, _ = fmt.Fprintln(os.Stdout, `{"id":"wrong","result":"ok"}`)
			return
		}
		if mode == "invalid-json" {
			_, _ = fmt.Fprintln(os.Stdout, `not json`)
			return
		}
		if mode == "fast-exit" {
			_, _ = fmt.Fprintf(os.Stdout, `{"id":%q,"result":"ok"}`+"\n", req.ID)
			os.Exit(0)
		}
		if mode == "progress-flood" {
			for i := 0; i < 100000; i++ {
				_, _ = fmt.Fprintf(os.Stdout, `{"id":%q,"method":"tool.progress","params":{"text":"update %d"}}`+"\n", req.ID, i)
			}
			for {
				time.Sleep(time.Hour)
			}
		}
		if mode == "progress" {
			_, _ = fmt.Fprintf(os.Stdout, `{"id":"stale","method":"tool.progress","params":{"text":"stale"}}`+"\n")
			_, _ = fmt.Fprintf(os.Stdout, `{"id":%q,"method":"tool.progress","params":{"text":"api_key=shh"}}`+"\n", req.ID)
			_, _ = fmt.Fprintf(os.Stdout, `{"id":%q,"result":"ok"}`+"\n", req.ID)
			return
		}
		if mode == "progress-fast-exit" {
			_, _ = fmt.Fprintf(os.Stdout, `{"id":%q,"method":"tool.progress","params":{"text":"before final"}}`+"\n", req.ID)
			_, _ = fmt.Fprintf(os.Stdout, `{"id":%q,"result":"ok"}`+"\n", req.ID)
			os.Exit(0)
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"id":%q,"result":"ok"}`+"\n", req.ID)
	}
}

func TestProcessWorkerLifecycleIsFireAndForgetBesideToolCalls(t *testing.T) {
	t.Setenv("PK_EXTENSION_PROCESS_HELPER", "lifecycle-slow")
	seen := filepath.Join(t.TempDir(), "lifecycle-seen")
	t.Setenv("PK_EXTENSION_LIFECYCLE_SEEN", seen)
	worker := testProcessWorker(t)
	defer worker.Close()
	notifier, ok := worker.(LifecycleNotifier)
	if !ok {
		t.Fatal("process worker does not support one-way lifecycle notifications")
	}
	start := time.Now()
	if !notifier.NotifyLifecycle(LifecycleEvent{Type: LifecycleRunStart, SessionID: "session", Model: "gpt-6-luna", Workspace: t.TempDir()}) {
		t.Fatal("lifecycle notification was not queued")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("notification enqueue blocked for %s", elapsed)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(seen); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process did not receive lifecycle notification")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var result string
	if err := worker.Call(ctx, "tool.execute", ToolExecuteParams{Name: "tool", CallID: "call"}, &result); err != nil {
		t.Fatalf("tool call failed after observer notification: %v", err)
	}
	if result != "ok" {
		t.Fatalf("tool result=%q", result)
	}
}

func TestProcessWorkerExitReleasesQueuedLifecycleDrainWaiters(t *testing.T) {
	done := make(chan struct{})
	close(done)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	worker := &processWorker{done: done, stdin: writer, lifecycleQueue: make(chan []byte, 2)}
	for i := 0; i < cap(worker.lifecycleQueue); i++ {
		worker.lifecycleWG.Add(1)
		worker.lifecycleQueue <- []byte("{}\n")
	}
	worker.writeLifecycleNotifications()
	completed := make(chan struct{})
	go func() { worker.lifecycleWG.Wait(); close(completed) }()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("queued lifecycle drain waiter leaked after process exit")
	}
}

type failingLifecycleWriter struct{}

func (failingLifecycleWriter) Write([]byte) (int, error) { return 0, errors.New("pipe failed") }
func (failingLifecycleWriter) Close() error              { return nil }

func TestProcessWorkerStopsNotificationIntakeAfterWriterFailure(t *testing.T) {
	worker := &processWorker{done: make(chan struct{}), stdin: failingLifecycleWriter{}, lifecycleQueue: make(chan []byte, 1)}
	if !worker.NotifyLifecycle(LifecycleEvent{Type: LifecycleRunEnd}) {
		t.Fatal("initial notification was not queued")
	}
	worker.writeLifecycleNotifications()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.WaitLifecycle(ctx); err != nil {
		t.Fatalf("wait after writer failure: %v", err)
	}
	if worker.NotifyLifecycle(LifecycleEvent{Type: LifecycleRunEnd}) {
		t.Fatal("notification accepted after transport writer failed")
	}
}

func TestProcessWorkerDoesNotLoseFinalAfterProgressOnExit(t *testing.T) {
	t.Setenv("PK_EXTENSION_PROCESS_HELPER", "progress-fast-exit")
	for i := 0; i < 30; i++ {
		worker := testProcessWorker(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ctx = context.WithValue(ctx, progressReporterKey{}, func(string) bool { return true })
		var result string
		err := worker.Call(ctx, "tool.execute", ToolExecuteParams{Name: "tool", CallID: "model-call"}, &result)
		cancel()
		_ = worker.Close()
		if err != nil || result != "ok" {
			t.Fatalf("iteration %d: result=%q err=%v", i, result, err)
		}
	}
}

func TestProcessWorkerProgressMatchesActiveRequestAndRedacts(t *testing.T) {
	t.Setenv("PK_EXTENSION_PROCESS_HELPER", "progress")
	worker := testProcessWorker(t)
	defer worker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got []string
	ctx = context.WithValue(ctx, progressReporterKey{}, func(text string) bool { got = append(got, text); return true })
	var result string
	params := ToolExecuteParams{Name: "ext_tool", CallID: "model-call-7"}
	if err := worker.Call(ctx, "tool.execute", params, &result); err != nil {
		t.Fatal(err)
	}
	if result != "ok" {
		t.Fatalf("result=%q", result)
	}
	if len(got) != 1 || got[0] != "api_key=[redacted]" {
		t.Fatalf("progress=%q", got)
	}
}

func TestProcessWorkerProgressFloodDoesNotBlockCancellation(t *testing.T) {
	t.Setenv("PK_EXTENSION_PROCESS_HELPER", "progress-flood")
	worker := testProcessWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var result string
	err := worker.Call(ctx, "tool.execute", ToolExecuteParams{Name: "ext_tool", CallID: "call"}, &result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call error=%v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- worker.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked after progress flood")
	}
}

func TestProcessWorkerReturnsLastLineBeforeExit(t *testing.T) {
	t.Setenv("PK_EXTENSION_PROCESS_HELPER", "fast-exit")
	for i := 0; i < 30; i++ {
		worker := testProcessWorker(t)
		var result string
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := worker.Call(ctx, "test", struct{}{}, &result)
		cancel()
		_ = worker.Close()
		if err != nil || result != "ok" {
			t.Fatalf("iteration %d: result=%q err=%v", i, result, err)
		}
	}
}

func TestProcessWorkerRejectsBadResponseAndIsolatesCrash(t *testing.T) {
	for _, mode := range []string{"wrong-id", "invalid-json", "crash"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PK_EXTENSION_PROCESS_HELPER", mode)
			worker := testProcessWorker(t)
			defer worker.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var result string
			if err := worker.Call(ctx, "test", struct{}{}, &result); err == nil {
				t.Fatal("expected extension process error")
			}
		})
	}
}

func TestProcessWorkerCancelsBlockedStdinWrite(t *testing.T) {
	t.Setenv("PK_EXTENSION_PROCESS_HELPER", "hang")
	worker := testProcessWorker(t)
	defer worker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	large := strings.Repeat("x", maxMessageSize-1024)
	started := time.Now()
	var result string
	err := worker.Call(ctx, "test", struct {
		Value string `json:"value"`
	}{large}, &result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected write to be interrupted by deadline, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancellation blocked for %s: %v", elapsed, err)
	}
}

func TestWorkspaceStatsSampleOverRealProcessProtocol(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(t.TempDir(), "workspace-stats")
	build := exec.Command("go", "build", "-o", workerPath, "./examples/workspace_stats")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sample worker: %v\n%s", err, output)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := statsManifest("workspace-stats", "workspace_stats")
	manifest.Executable = workerPath
	host, report, err := NewHost(context.Background(), workspace, []Manifest{manifest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 {
		t.Fatalf("sample worker did not load: %+v", report)
	}
	result, err := host.ExecuteTool(context.Background(), "workspace_stats", "call-real", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "1 files, 0 directories, 3 bytes" {
		t.Fatalf("sample result: %+v", result)
	}
}

func testProcessWorker(t *testing.T) Worker {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{ID: "test-worker", Executable: executable, Args: []string{"-test.run=^TestExtensionProcessHelper$"}}
	worker, err := ProcessFactory(context.Background(), manifest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return worker
}
