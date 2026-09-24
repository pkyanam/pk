package filetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func strptr(value string) *string { return &value }

func TestWriteFileCreateOverwriteAndPermissions(t *testing.T) {
	workspace := t.TempDir()
	if _, err := apply(context.Background(), workspace, request{Action: "WriteFile", Args: args{Path: "new.go", Content: strptr("package sample\n")}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "new.go")
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "package sample\n" {
		t.Fatalf("new file = %q, err=%v", contents, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("new-file mode = %v, err=%v", info, err)
	}
	if _, err := apply(context.Background(), workspace, request{Action: "WriteFile", Args: args{Path: "new.go", Content: strptr("changed")}}); err == nil {
		t.Fatal("WriteFile replaced an existing file without overwrite=true")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(context.Background(), workspace, request{Action: "WriteFile", Args: args{Path: "new.go", Content: strptr("changed"), Overwrite: true}}); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(path)
	if err != nil || string(contents) != "changed" {
		t.Fatalf("overwritten file = %q, err=%v", contents, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("overwritten mode = %v, err=%v", info, err)
	}
}

func TestEditFileExactMatchAmbiguityAndStaleProtection(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "main.go")
	original := "line one\r\nline two\r\nline two\r\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	base := args{Path: "main.go", OldString: "line two", NewString: strptr("updated")}
	if _, err := apply(context.Background(), workspace, request{Action: "EditFile", Args: base}); err == nil || !strings.Contains(err.Error(), "2 locations") {
		t.Fatalf("ambiguous edit error = %v", err)
	}
	assertContents(t, path, original)
	base.ReplaceAll = true
	if _, err := apply(context.Background(), workspace, request{Action: "EditFile", Args: base}); err != nil {
		t.Fatal(err)
	}
	updated := "line one\r\nupdated\r\nupdated\r\n"
	assertContents(t, path, updated)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("edited mode = %v, err=%v", info, err)
	}

	digest := sha256.Sum256([]byte(updated))
	base = args{Path: "main.go", OldString: "line one", NewString: strptr("first"), ExpectedSHA256: hex.EncodeToString(digest[:])}
	if _, err := apply(context.Background(), workspace, request{Action: "EditFile", Args: base}); err != nil {
		t.Fatal(err)
	}
	base.ExpectedSHA256 = strings.Repeat("0", 64)
	if _, err := apply(context.Background(), workspace, request{Action: "EditFile", Args: base}); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("stale edit error = %v", err)
	}
	assertContents(t, path, "first\r\nupdated\r\nupdated\r\n")
}

func TestFileOperationsFailWithoutChangingOnMissingOrInvalidInput(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	original := "alpha alpha\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args args
	}{
		{name: "missing match", args: args{Path: "file.txt", OldString: "missing", NewString: strptr("x")}},
		{name: "missing replacement field", args: args{Path: "file.txt", OldString: "alpha"}},
		{name: "required content", args: args{Path: "other.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action := "EditFile"
			if tc.name == "required content" {
				action = "WriteFile"
			}
			if _, err := apply(context.Background(), workspace, request{Action: action, Args: tc.args}); err == nil {
				t.Fatal("invalid file operation unexpectedly succeeded")
			}
			assertContents(t, path, original)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := apply(ctx, workspace, request{Action: "WriteFile", Args: args{Path: "canceled.txt", Content: strptr("no")}}); err == nil {
		t.Fatal("canceled write unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(workspace, "canceled.txt")); !os.IsNotExist(err) {
		t.Fatalf("canceled write left a target: %v", err)
	}
}

func TestFilePathConfinesWorkspaceAndRejectsSymlinks(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(context.Background(), workspace, request{Action: "WriteFile", Args: args{Path: "../outside.txt", Content: strptr("no")}}); err == nil {
		t.Fatal("path traversal unexpectedly succeeded")
	}
	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := apply(context.Background(), workspace, request{Action: "WriteFile", Args: args{Path: "escape/new.txt", Content: strptr("no")}}); err == nil {
		t.Fatal("parent symlink unexpectedly succeeded")
	}
	if _, err := apply(context.Background(), workspace, request{Action: "EditFile", Args: args{Path: filepath.Join(workspace, "escape", "secret.txt"), OldString: "private", NewString: strptr("changed")}}); err == nil {
		t.Fatal("absolute symlink path unexpectedly succeeded")
	}
	assertContents(t, outsideFile, "private")
}

func TestDecoratorRegistersToolsAndPreservesBase(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{}, "Bash")
	registry := Decorator(t.TempDir())(base)
	for _, name := range []string{"Read", "WriteFile", "EditFile", "Bash"} {
		if _, ok := registry.Resolve(name); !ok {
			t.Fatalf("missing tool %s", name)
		}
	}
	if len(registry.StaticDefinitions()) != 4 {
		t.Fatalf("definitions = %d", len(registry.StaticDefinitions()))
	}
	if Decorator("")(base) != base {
		t.Fatal("empty workspace changed registry")
	}
}

func TestRemoteJobPublishesAwaitingBeforeCompleted(t *testing.T) {
	workspace := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	h := newHandler(ctx, workspace, nil, "")
	defer func() { cancel(); h.Wait() }()
	content := "done"
	data, err := json.Marshal(request{Action: "WriteFile", Args: args{Path: "result.txt", Content: &content}})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: planType, Version: planVersion, Data: jsontext.Value(data)})
	if err != nil {
		t.Fatal(err)
	}
	job := operation.Operation{ID: "write-1", Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, State: spec.State, MaxOutputLength: spec.MaxOutputLength}
	if err := h.AddRemoteJob(job); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-h.RemoteJobUpdates():
		if update.Status != operation.StatusAwaiting {
			t.Fatalf("first update status=%s, want awaiting", update.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for awaiting update")
	}
	select {
	case update := <-h.RemoteJobUpdates():
		if update.Status != operation.StatusCompleted {
			t.Fatalf("terminal update status=%s, want completed", update.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for completed update")
	}
}

func TestLateCancellationDoesNotReportCommittedWriteCanceled(t *testing.T) {
	if shouldReportCanceled(true, nil) {
		t.Fatal("late cancellation after successful atomic write must remain completed")
	}
	if !shouldReportCanceled(true, context.Canceled) {
		t.Fatal("cancellation before commit should report canceled")
	}
	if shouldReportCanceled(false, context.Canceled) {
		t.Fatal("non-cancellation failure must remain a failure")
	}
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("contents=%q err=%v want=%q", got, err, want)
	}
}
