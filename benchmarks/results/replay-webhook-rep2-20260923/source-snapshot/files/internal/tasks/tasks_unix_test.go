//go:build !windows

package tasks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetachedWorkerFollowAndSteer(t *testing.T) {
	if os.Getenv("PK_TASK_STORE") != "" {
		id := os.Getenv("PK_TASK_ID")
		mode := os.Getenv("PK_TASK_TEST_MODE")
		err := RunWorker(context.Background(), id, func(ctx context.Context, options WorkerOptions, out io.Writer) error {
			if mode == "steer" {
				if _, err := io.WriteString(out, "worker-ready\n"); err != nil {
					return err
				}
				if options.Resume {
					return nil
				}
				select {
				case input, ok := <-options.Inputs:
					if !ok {
						return errors.New("input stream closed")
					}
					_, err := io.WriteString(out, "follow-up: "+input.Text+"\n")
					input.Ack(nil)
					return err
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
					return errors.New("timed out waiting for follow-up")
				}
			}
			if options.Resume {
				if options.SessionID != "session-before-crash" || !strings.HasPrefix(options.Prompt, "Continue the previous task") {
					return errors.New("resume did not preserve session continuation")
				}
				return nil
			}
			options.OnSession("session-before-crash")
			<-ctx.Done()
			return ctx.Err()
		})
		if err != nil {
			os.Exit(3)
		}
		return
	}

	root := t.TempDir()
	workspace := filepath.Join(root, "new-workspace")
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TASK_TEST_BINARY", helper)
	t.Setenv("PK_TASK_TEST_MODE", "steer")
	script := filepath.Join(root, "worker.sh")
	body := "#!/bin/sh\nPK_TASK_ID=\"$2\" exec \"$TASK_TEST_BINARY\" -test.run=^TestDetachedWorkerFollowAndSteer$\n"
	if err = os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	store := Store{Root: filepath.Join(root, "tasks")}
	task, err := store.Start(context.Background(), StartOptions{ID: "detached-check", Prompt: "do a task", Workspace: workspace, ProviderID: "test-provider", Executable: script})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(workspace); err != nil {
		t.Fatalf("workspace was not created: %v", err)
	}
	if task.Status != StatusRunning || task.PID <= 0 {
		t.Fatalf("unexpected launch state: %+v", task)
	}
	loadedTask, err := store.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedTask.ProviderID != "test-provider" || loadedTask.Options.ProviderID != "test-provider" {
		t.Fatalf("task did not persist provider identity: task=%q options=%q", loadedTask.ProviderID, loadedTask.Options.ProviderID)
	}
	if _, err = store.Resume(context.Background(), task.ID); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("resume of live worker = %v, want ErrAlreadyRunning", err)
	}
	if err = store.SendInput(task.ID, "add a test"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cursor, err := store.Follow(ctx, task.ID, 0, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "worker-ready\n") || !strings.Contains(output.String(), "follow-up: add a test\n") {
		t.Fatalf("unexpected attached output: %q", output.String())
	}
	finished, err := store.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusSucceeded {
		logBytes, _ := os.ReadFile(filepath.Join(store.Root, task.ID, "worker.log"))
		inputBytes, _ := os.ReadFile(filepath.Join(store.Root, task.ID, "inputs.jsonl"))
		eventBytes, _ := os.ReadFile(filepath.Join(store.Root, task.ID, "events.jsonl"))
		t.Fatalf("task status=%s error=%s log=%q inputs=%q events=%q", finished.Status, finished.Error, logBytes, inputBytes, eventBytes)
	}
	if cursor == 0 || finished.LastEvent < cursor {
		t.Fatalf("cursor=%d last event=%d", cursor, finished.LastEvent)
	}
	info, err := os.Stat(filepath.Join(store.Root, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("task directory is not private: %o", info.Mode().Perm())
	}
}

func TestResumeAfterWorkerExitUsesSessionWithoutDuplicateWorker(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TASK_TEST_BINARY", helper)
	t.Setenv("PK_TASK_TEST_MODE", "recover")
	script := filepath.Join(root, "worker.sh")
	body := "#!/bin/sh\nPK_TASK_ID=\"$2\" exec \"$TASK_TEST_BINARY\" -test.run=^TestDetachedWorkerFollowAndSteer$\n"
	if err = os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	store := Store{Root: filepath.Join(root, "tasks")}
	task, err := store.Start(context.Background(), StartOptions{ID: "recover-check", Prompt: "original request", Workspace: workspace, Executable: script})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		current, e := store.Get(task.ID)
		if e == nil && current.SessionID == "session-before-crash" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("session was not persisted before kill: task=%+v err=%v", current, e)
		case <-time.After(20 * time.Millisecond):
		}
	}
	process, err := os.FindProcess(task.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Kill(); err != nil {
		t.Fatal(err)
	}
	for {
		current, e := store.Get(task.ID)
		if e == nil && current.Status == StatusInterrupted {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("worker did not reconcile as interrupted: status=%s err=%v", current.Status, e)
		case <-time.After(25 * time.Millisecond):
		}
	}
	resumed, err := store.Resume(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != StatusRunning {
		t.Fatalf("resumed task status=%s", resumed.Status)
	}
	if _, err = store.Follow(ctx, task.ID, 0, io.Discard); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusSucceeded {
		logBytes, _ := os.ReadFile(filepath.Join(store.Root, task.ID, "worker.log"))
		t.Fatalf("resumed task status=%s error=%s workerlog=%q", finished.Status, finished.Error, logBytes)
	}
	for {
		busy, release, e := tryTaskLock(filepath.Join(store.Root, task.ID))
		if e == nil && !busy {
			release()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("worker lock remained held after completion")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
