package sessionmanager

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/presentation"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestListSearchesBoundedSessionMetadata(t *testing.T) {
	manager, sessionDir := fixtureManager(t)
	if _, err := createSession(t, sessionDir, "session-a", "Add webhook signing", "Implemented signature verification."); err != nil {
		t.Fatal(err)
	}
	items, err := manager.List(context.Background(), ListOptions{Query: "signature"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "session-a" || items[0].Title != "Add webhook signing" || items[0].Preview != "Implemented signature verification." || items[0].Workspace == "" {
		t.Fatalf("unexpected list result: %+v", items)
	}
	items, err = manager.List(context.Background(), ListOptions{Query: "missing"})
	if err != nil || len(items) != 0 {
		t.Fatalf("nonmatching list=%+v err=%v", items, err)
	}
}

func TestArchiveRestorePreservesSessionSnapshotAndCapturedOperation(t *testing.T) {
	manager, sessionDir := fixtureManager(t)
	const id = "session-archive"
	if _, err := createSession(t, sessionDir, id, "Archive me", "Final output"); err != nil {
		t.Fatal(err)
	}
	contextPath := contextSnapshotPath(sessionDir, id)
	if err := os.WriteFile(contextPath, []byte(`{"Version":1,"Workspace":"/workspace/example","SystemPrompt":"stable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	presentationRecord, err := presentation.NewRecord("review report", "review report\nattached extracted text", []attachments.Attachment{{Path: "report.txt", Kind: attachments.Text, ContentType: "text/plain"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := presentation.Save(sessionDir, id, "input-with-file", presentationRecord); err != nil {
		t.Fatal(err)
	}
	presentationPath := presentation.RecordPath(sessionDir, id, "input-with-file")
	operationDir := filepath.Join(sessionDir, "operations")
	compactionPath := filepath.Join(outputCompactionPath(operationDir, id), "decision.json")
	if err := os.MkdirAll(filepath.Dir(compactionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(compactionPath, []byte("summary"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(operationDir, "op-capture-1", "out")
	if err := os.MkdirAll(filepath.Dir(capture), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capture, []byte("complete output"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := localfile.New(sessionDir)
	state := `{"BaseDirectory":"` + operationDir + `","OutPath":"` + capture + `","Result":{"Out":"complete output"}}`
	call := sessionstore.ToolCallStatus{TurnID: "turn-1", CallID: "call-1", Status: tool.CallStatus{WaitingFor: []operation.ID{"op-capture-1"}}, Operations: []operation.Operation{{ID: "op-capture-1", Type: operation.TypeShell, Version: operation.VersionShell, Status: operation.StatusCompleted, MaxOutputLength: 1024, State: jsontext.Value(state)}}}
	if err := store.AppendToolCallStatus(context.Background(), session.ID(id), call); err != nil {
		t.Fatal(err)
	}
	result := manager.Archive(context.Background(), []string{id}, nil)
	if len(result) != 1 || !result[0].OK {
		t.Fatalf("archive result: %+v", result)
	}
	for _, path := range []string{filepath.Join(sessionDir, id+".session.jsonl"), presentationPath, contextPath, compactionPath, capture} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("live artifact still exists at %s (err=%v)", path, err)
		}
	}
	trash, err := manager.ListTrash(context.Background(), "webhook")
	if err != nil || len(trash) != 0 {
		t.Fatalf("wrong query matched trash=%+v err=%v", trash, err)
	}
	trash, err = manager.ListTrash(context.Background(), "Archive me")
	if err != nil || len(trash) != 1 || trash[0].TrashID != result[0].TrashID {
		t.Fatalf("trash list=%+v err=%v", trash, err)
	}
	restored := manager.Restore(context.Background(), []string{result[0].TrashID}, nil)
	if len(restored) != 1 || !restored[0].OK {
		t.Fatalf("restore result: %+v", restored)
	}
	for path, want := range map[string]string{
		filepath.Join(sessionDir, id+".session.jsonl"): "session",
		contextPath:    "stable",
		compactionPath: "summary",
		capture:        "complete output",
	} {
		if path == filepath.Join(sessionDir, id+".session.jsonl") {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("session log not restored: %v", err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(got), want) {
			t.Errorf("restored %s = %q, %v; want containing %q", path, got, err, want)
		}
	}
	restoredPresentation, err := presentation.Load(sessionDir, id, "input-with-file")
	if err != nil || !restoredPresentation.Matches("review report\nattached extracted text") || len(restoredPresentation.Attachments) != 1 || restoredPresentation.Attachments[0].Name != "report.txt" {
		t.Fatalf("restored attachment presentation=%+v err=%v", restoredPresentation, err)
	}
}

func TestRestoreCollisionLeavesArchiveRecoverable(t *testing.T) {
	manager, sessionDir := fixtureManager(t)
	if _, err := createSession(t, sessionDir, "session-collision", "Collision", "Answer"); err != nil {
		t.Fatal(err)
	}
	archived := manager.Archive(context.Background(), []string{"session-collision"}, nil)
	if len(archived) != 1 || !archived[0].OK {
		t.Fatalf("archive: %+v", archived)
	}
	logPath := filepath.Join(sessionDir, "session-collision.session.jsonl")
	if err := os.WriteFile(logPath, []byte("unrelated live file"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := manager.Restore(context.Background(), []string{archived[0].TrashID}, nil)
	if restored[0].OK || !strings.Contains(restored[0].Error, "both archived and live copies") {
		t.Fatalf("restore over collision=%+v", restored)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil || string(contents) != "unrelated live file" {
		t.Fatalf("collision target changed: %q err=%v", contents, err)
	}
	if entries, err := manager.ListTrash(context.Background(), "session-collision"); err != nil || len(entries) != 1 {
		t.Fatalf("archive no longer recoverable: %+v err=%v", entries, err)
	}
}

func TestPurgeRequiresExplicitTrashIDAndDoesNotFollowSymlinks(t *testing.T) {
	manager, sessionDir := fixtureManager(t)
	if _, err := createSession(t, sessionDir, "session-purge", "Purge fixture", "Answer"); err != nil {
		t.Fatal(err)
	}
	record, err := presentation.NewRecord("Purge fixture", "Purge fixture\nfile note", []attachments.Attachment{{Path: "fixture.txt", Kind: attachments.Text}})
	if err != nil {
		t.Fatal(err)
	}
	if err := presentation.Save(sessionDir, "session-purge", "input-1", record); err != nil {
		t.Fatal(err)
	}
	archived := manager.Archive(context.Background(), []string{"session-purge"}, nil)
	if len(archived) != 1 || !archived[0].OK {
		t.Fatalf("archive: %+v", archived)
	}
	entryDir := filepath.Join(filepath.Dir(sessionDir), "trash", "sessions", archived[0].TrashID)
	payload := filepath.Join(entryDir, "payload")
	sentinel := filepath.Join(t.TempDir(), "outside-sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(payload, "untrusted-link")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	blocked := manager.Purge(context.Background(), []string{archived[0].TrashID}, nil)
	if blocked[0].OK || !strings.Contains(blocked[0].Error, "refusing unsafe trash entry") {
		t.Fatalf("purge with symlink=%+v", blocked)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "preserve me" {
		t.Fatalf("purge followed symlink: %q err=%v", got, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	blocked = manager.Purge(context.Background(), []string{archived[0].TrashID}, map[string]bool{"session-purge": true})
	if blocked[0].OK || !strings.Contains(blocked[0].Error, "currently in use") {
		t.Fatalf("purge active session=%+v", blocked)
	}
	removed := manager.Purge(context.Background(), []string{archived[0].TrashID}, nil)
	if len(removed) != 1 || !removed[0].OK {
		t.Fatalf("purge: %+v", removed)
	}
	if _, err := os.Stat(entryDir); !os.IsNotExist(err) {
		t.Fatalf("trash entry remains after purge, err=%v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "preserve me" {
		t.Fatalf("purge changed unrelated file: %q err=%v", got, err)
	}
}

func TestArchiveRefusesCallerAndLeaseActiveSessions(t *testing.T) {
	manager, sessionDir := fixtureManager(t)
	if _, err := createSession(t, sessionDir, "active-session", "Active", "Answer"); err != nil {
		t.Fatal(err)
	}
	blocked := manager.Archive(context.Background(), []string{"active-session"}, map[string]bool{"active-session": true})
	if blocked[0].OK || !strings.Contains(blocked[0].Error, "currently in use") {
		t.Fatalf("caller-active archive result=%+v", blocked)
	}
	lease, err := sessionlock.Acquire(sessionDir, "active-session")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	blocked = manager.Archive(context.Background(), []string{"active-session"}, nil)
	if blocked[0].OK || !strings.Contains(blocked[0].Error, "currently in use") {
		t.Fatalf("lease-active archive result=%+v", blocked)
	}
}

func TestRunnerLeaseBlocksArchiveUntilRunReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessionDir, workspace := filepath.Join(t.TempDir(), "sessions"), t.TempDir()
	adapter := &blockingAdapter{entered: make(chan struct{}), release: make(chan struct{})}
	sessionID := make(chan string, 1)
	runDone := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{
			Prompt: "finish after the gate opens", SessionDir: sessionDir, Workspace: workspace,
			Model: "gpt-6-luna", Effort: "medium", Adapter: adapter,
			OnSession: func(id string) { sessionID <- id },
		})
		runDone <- err
	}()
	select {
	case <-adapter.entered:
	case <-ctx.Done():
		t.Fatal("runner never entered model response")
	}
	id := <-sessionID
	manager := Manager{SessionDir: sessionDir}
	result := manager.Archive(ctx, []string{id}, nil)
	if len(result) != 1 || result[0].OK || !strings.Contains(result[0].Error, "currently in use") {
		t.Fatalf("archive during runner response=%+v", result)
	}
	close(adapter.release)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runner: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("runner failed to settle after adapter release")
	}
	result = manager.Archive(ctx, []string{id}, nil)
	if len(result) != 1 || !result[0].OK {
		t.Fatalf("archive after runner returned=%+v", result)
	}
}

func TestArchiveBlockedWhileCrossProcessLeaseHeld(t *testing.T) {
	if os.Getenv("PK_SESSION_ARCHIVE_HELPER") == "1" {
		dir := os.Getenv("PK_SESSION_ARCHIVE_DIR")
		lease, err := sessionlock.Acquire(dir, "cross-process-session")
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Release()
		if err := os.WriteFile(filepath.Join(dir, "lease-ready"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "lease-stop")); err == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	manager, sessionDir := fixtureManager(t)
	if _, err := createSession(t, sessionDir, "cross-process-session", "Live", "Do not archive"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestArchiveBlockedWhileCrossProcessLeaseHeld$")
	cmd.Env = append(os.Environ(), "PK_SESSION_ARCHIVE_HELPER=1", "PK_SESSION_ARCHIVE_DIR="+sessionDir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(filepath.Join(sessionDir, "lease-stop"), []byte("stop"), 0o600)
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(sessionDir, "lease-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process did not acquire session lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
	result := manager.Archive(context.Background(), []string{"cross-process-session"}, nil)
	if len(result) != 1 || result[0].OK || !strings.Contains(result[0].Error, "currently in use") {
		t.Fatalf("archive while another process holds the lease: %+v", result)
	}
}

func TestArchiveRejectsPathTraversalAndPartialBatch(t *testing.T) {
	manager, sessionDir := fixtureManager(t)
	if _, err := createSession(t, sessionDir, "good-session", "Good", "Answer"); err != nil {
		t.Fatal(err)
	}
	results := manager.Archive(context.Background(), []string{"../outside", "good-session", "good-session"}, nil)
	if len(results) != 3 || results[0].OK || !results[1].OK || results[2].OK || !strings.Contains(results[2].Error, "duplicate") {
		t.Fatalf("partial batch results=%+v", results)
	}
}

func fixtureManager(t *testing.T) (Manager, string) {
	t.Helper()
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	return Manager{SessionDir: sessionDir}, sessionDir
}

func createSession(t *testing.T, sessionDir, id, prompt, answer string) (Session, error) {
	t.Helper()
	store, err := localfile.New(sessionDir)
	if err != nil {
		return Session{}, err
	}
	if _, err := store.Create(context.Background(), session.ID(id)); err != nil {
		return Session{}, err
	}
	if err := store.AppendInput(context.Background(), session.ID(id), inbox.Input{ID: inbox.ID("prompt-1"), Kind: inbox.InputExternal, Payload: jsontext.Value(mustJSON(t, prompt))}); err != nil {
		return Session{}, err
	}
	if err := store.AppendTurn(context.Background(), session.ID(id), session.Turn{ID: "turn-1"}); err != nil {
		return Session{}, err
	}
	if err := store.AppendModelResponse(context.Background(), session.ID(id), sessionstore.ModelResponse{TurnID: "turn-1", Response: llm.Response{ID: "response-1", Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: answer}}}}}); err != nil {
		return Session{}, err
	}
	if err := os.WriteFile(contextSnapshotPath(sessionDir, id), []byte(`{"Version":1,"Workspace":"/workspace/test"}`), 0o600); err != nil {
		return Session{}, err
	}
	entries, err := (Manager{SessionDir: sessionDir}).List(context.Background(), ListOptions{})
	if err != nil {
		return Session{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return Session{}, os.ErrNotExist
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type blockingAdapter struct {
	entered chan struct{}
	release chan struct{}
}

func (a *blockingAdapter) Respond(ctx context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	close(a.entered)
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-a.release:
		return llm.Response{ID: "response-final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "Done."}}}}, nil
	}
}
