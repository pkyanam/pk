package filetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/operation"
)

func testJournalSession() string { return "0123456789abcdef0123456789abcdef" }

func stringPtr(value string) *string { return &value }

// runJournaledJob submits one file request through a journaling handler and
// waits for the terminal operation status, mirroring the coordinator flow.
func runJournaledJob(t *testing.T, workspace string, store *workspacejournal.Store, req request) (string, error) {
	t.Helper()
	return runJournaledJobWithID(t, "test-op", workspace, store, req)
}

// runJournaledJobWithID submits one request under an explicit operation ID.
func runJournaledJobWithID(t *testing.T, opID, workspace string, store *workspacejournal.Store, req request) (string, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writer *handler
	for _, h := range HandlerFactoryWithJournal(workspace, store, testJournalSession())(ctx) {
		if typed, ok := h.(*handler); ok {
			writer = typed
		}
	}
	if writer == nil {
		t.Fatal("expected the writer handler")
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: planType, Version: planVersion, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	op := operation.Operation{ID: operation.ID(opID), Type: spec.Type, Version: spec.Version, State: spec.State, MaxOutputLength: 1024}
	addErr := writer.AddRemoteJob(op)
	if addErr != nil {
		// Synchronous rejection (e.g. invalid arguments): the operation was
		// never scheduled and nothing is journaled.
		return "", addErr
	}
	for update := range writer.RemoteJobUpdates() {
		if update.ID != op.ID {
			continue
		}
		switch update.Status {
		case operation.StatusCompleted:
			state, err := operation.DecodeRemoteJobState(update)
			if err != nil {
				return "", err
			}
			return state.TerminalResult, nil
		case operation.StatusFailed, operation.StatusCanceled:
			state, err := operation.DecodeRemoteJobState(update)
			if err != nil {
				return "", err
			}
			if state.TerminalError != "" {
				return "", errors.New(state.TerminalError)
			}
			return "", fmt.Errorf("operation ended with status %s", update.Status)
		}
	}
	return "", errors.New("handler updates closed before a terminal status")
}

func TestJournalRecordsCompletedWrite(t *testing.T) {
	workspace := t.TempDir()
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	content := "hello"
	if _, err := runJournaledJob(t, workspace, store, request{Action: "WriteFile", Args: args{Path: "greet.txt", Content: &content}}); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(testJournalSession())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Status != workspacejournal.StatusCompleted {
		t.Fatalf("expected one completed op: %+v", ops)
	}
	if ops[0].Path != "greet.txt" || ops[0].Pre != nil {
		t.Fatalf("unexpected op: %+v", ops[0])
	}
	post, err := store.ReadObject(*ops[0].Post)
	if err != nil {
		t.Fatal(err)
	}
	if string(post) != "hello" {
		t.Fatalf("unexpected post image %q", post)
	}
}

func TestJournalRecordsEditPreimage(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "code.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runJournaledJob(t, workspace, store, request{Action: "EditFile", Args: args{Path: "code.txt", OldString: "beta", NewString: stringPtr("gamma")}}); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(testJournalSession())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Pre == nil || ops[0].Post == nil {
		t.Fatalf("expected a completed op with both images: %+v", ops)
	}
	pre, err := store.ReadObject(*ops[0].Pre)
	if err != nil {
		t.Fatal(err)
	}
	if string(pre) != "alpha\nbeta\n" {
		t.Fatalf("preimage mismatch: %q", pre)
	}
	post, err := store.ReadObject(*ops[0].Post)
	if err != nil {
		t.Fatal(err)
	}
	if string(post) != "alpha\ngamma\n" {
		t.Fatalf("postimage mismatch: %q", post)
	}
}

func TestFailedPreconditionIsNotJournaled(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "f.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runJournaledJob(t, workspace, store, request{Action: "EditFile", Args: args{Path: "f.txt", OldString: "absent", NewString: stringPtr("x")}}); err == nil {
		t.Fatal("expected an edit failure")
	}
	ops, err := store.List(testJournalSession())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("precondition failures must not be journaled: %+v", ops)
	}
}

func TestInvalidArgumentsAreNotJournaled(t *testing.T) {
	workspace := t.TempDir()
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	// Invalid arguments are rejected by AddRemoteJob before any operation is
	// scheduled, so the error surfaces synchronously and nothing is journaled.
	empty := ""
	_, syncErr := runJournaledJobWithID(t, "test-op", workspace, store, request{Action: "WriteFile", Args: args{Path: "", Content: &empty}})
	if syncErr == nil {
		t.Fatal("expected a validation failure")
	}
	ops, _ := store.List(testJournalSession())
	if len(ops) != 0 {
		t.Fatalf("invalid requests must not be journaled: %+v", ops)
	}
	_ = syncErr
}

func TestWorkspaceDeltaSummarizesAndDiffs(t *testing.T) {
	workspace := t.TempDir()
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	content := "alpha\nbeta\n"
	if _, err := runJournaledJob(t, workspace, store, request{Action: "WriteFile", Args: args{Path: "notes.md", Content: &content}}); err != nil {
		t.Fatal(err)
	}
	updated := "alpha changed\nbeta\n"
	if _, err := runJournaledJobWithID(t, "test-op-2", workspace, store, request{Action: "WriteFile", Args: args{Path: "notes.md", Content: &updated, Overwrite: true}}); err != nil {
		t.Fatal(err)
	}
	summary, err := runDelta(store, testJournalSession(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "notes.md") || !strings.Contains(summary, "created") || !strings.Contains(summary, "modified") {
		t.Fatalf("summary incomplete: %s", summary)
	}
	if !strings.Contains(summary, "unobserved") {
		t.Fatalf("coverage note missing: %s", summary)
	}
	if !strings.Contains(summary, "Journal cursor:") || !strings.Contains(summary, "inclusive as-of sequence") {
		t.Fatalf("latest cursor hint missing from summary: %s", summary)
	}
	diffOutput, err := runDelta(store, testJournalSession(), `{"diff":true,"path":"notes.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diffOutput, "- alpha") || !strings.Contains(diffOutput, "+ alpha changed") {
		t.Fatalf("diff wrong: %s", diffOutput)
	}
	if len(diffOutput) > (16<<10)+200 {
		t.Fatal("diff exceeded its bound")
	}
	if _, err := runDelta(store, testJournalSession(), `{"diff":true}`); err == nil {
		t.Fatal("expected diff without path/op_id to fail")
	}
	if _, err := runDelta(store, testJournalSession(), `{"path":"x"}`); err == nil {
		t.Fatal("expected path without diff to fail")
	}
	if _, err := runDelta(store, testJournalSession(), `{"cursor":"abc"}`); err == nil {
		t.Fatal("expected non-decimal cursor to fail")
	}
	if _, err := runDelta(store, testJournalSession(), `{"diff":true,"path":"../escape"}`); err == nil {
		t.Fatal("expected traversal path to fail")
	}
	if _, err := runDelta(store, testJournalSession(), `{"diff":true,"path":"missing.txt"}`); err == nil {
		t.Fatal("expected unknown path to fail")
	}
}

func TestWorkspaceDeltaCursor(t *testing.T) {
	workspace := t.TempDir()
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	first := "one"
	if _, err := runJournaledJob(t, workspace, store, request{Action: "WriteFile", Args: args{Path: "a.txt", Content: &first}}); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(testJournalSession())
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := fmt.Sprintf("%d", ops[0].Seq)
	second := "two"
	if _, err := runJournaledJobWithID(t, "test-op-2", workspace, store, request{Action: "WriteFile", Args: args{Path: "b.txt", Content: &second}}); err != nil {
		t.Fatal(err)
	}
	summary, err := runDelta(store, testJournalSession(), fmt.Sprintf(`{"cursor":%q}`, afterFirst))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "a.txt") || strings.Contains(summary, "b.txt") || !strings.Contains(summary, "Journal cursor: "+afterFirst) {
		t.Fatalf("cursor should show only ops up to the cursor: %s", summary)
	}
}

func TestWorkspaceDeltaDiffHonorsAsOfCursor(t *testing.T) {
	workspace := t.TempDir()
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	first := "one"
	if _, err := runJournaledJob(t, workspace, store, request{Action: "WriteFile", Args: args{Path: "a.txt", Content: &first}}); err != nil {
		t.Fatal(err)
	}
	ops, err := store.List(testJournalSession())
	if err != nil {
		t.Fatal(err)
	}
	firstSeq := fmt.Sprint(ops[0].Seq)
	second := "two"
	if _, err := runJournaledJobWithID(t, "test-op-2", workspace, store, request{Action: "EditFile", Args: args{Path: "a.txt", OldString: "one", NewString: &second}}); err != nil {
		t.Fatal(err)
	}
	ops, err = store.List(testJournalSession())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runDelta(store, testJournalSession(), fmt.Sprintf(`{"diff":true,"path":"a.txt","cursor":%q}`, firstSeq)); err == nil || !strings.Contains(err.Error(), "created file") {
		t.Fatalf("diff did not select the as-of create at cursor %s: %v", firstSeq, err)
	}
	latest, err := runDelta(store, testJournalSession(), `{"diff":true,"path":"a.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	secondSeq := fmt.Sprint(ops[len(ops)-1].Seq)
	asOfSecond, err := runDelta(store, testJournalSession(), fmt.Sprintf(`{"diff":true,"path":"a.txt","cursor":%q}`, secondSeq))
	if err != nil {
		t.Fatal(err)
	}
	if latest != asOfSecond || !strings.Contains(asOfSecond, "journal seq "+secondSeq) {
		t.Fatalf("cursor did not select the inclusive journal diff: latest=%q as-of=%q", latest, asOfSecond)
	}
}

func TestWorkspaceDeltaEmptyJournalMentionsCoverage(t *testing.T) {
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runDelta(store, testJournalSession(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No file-tool changes recorded") || !strings.Contains(out, "unobserved") {
		t.Fatalf("empty summary must state coverage: %s", out)
	}
	cutoff, err := runDelta(store, testJournalSession(), `{"cursor":"9"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cutoff, "No file-tool changes recorded as of cursor 9") || strings.Contains(cutoff, "since cursor 9") || !strings.Contains(cutoff, "Journal cursor: 9") {
		t.Fatalf("empty as-of summary should state cutoff and return its cursor: %s", cutoff)
	}
}

func TestWorkspaceDeltaToolDescriptionExplainsWhenAndHowToUseCursor(t *testing.T) {
	definition := DeltaDefinition().Tool
	if !strings.Contains(definition.Description, "After WriteFile/EditFile edits") || !strings.Contains(definition.Description, "not a changes-since cursor") {
		t.Fatalf("WorkspaceDelta does not explain its use or cursor semantics: %q", definition.Description)
	}
	cursor := definition.Parameters["properties"].(map[string]any)["cursor"].(map[string]any)["description"].(string)
	if !strings.Contains(cursor, "Inclusive as-of sequence") || !strings.Contains(cursor, "latest full retained summary") {
		t.Fatalf("cursor parameter guidance is incomplete: %q", cursor)
	}
	if len(definition.Description) > 500 {
		t.Fatalf("tool discoverability guidance is unexpectedly verbose: %d bytes", len(definition.Description))
	}
}

func TestWorkspaceDeltaRejectsTrailingJSONValues(t *testing.T) {
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runDelta(store, testJournalSession(), `{"cursor":"0"} {"cursor":"1"}`); err == nil || !strings.Contains(err.Error(), "one JSON value") {
		t.Fatalf("expected trailing JSON to be rejected, got %v", err)
	}
	if _, err := runDelta(store, testJournalSession(), "  {\"cursor\":\"0\"} \n"); err != nil {
		t.Fatalf("valid JSON with trailing whitespace rejected: %v", err)
	}
}

func TestPlainFactoryOmitsDeltaHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, h := range HandlerFactory(t.TempDir())(ctx) {
		if _, ok := h.(*deltaHandler); ok {
			t.Fatal("plain factory must not include the delta handler")
		}
	}
}

func TestJournalDisabledKeepsLegacyBehavior(t *testing.T) {
	workspace := t.TempDir()
	content := "hello"
	result, err := runJournaledJob(t, workspace, nil, request{Action: "WriteFile", Args: args{Path: "greet.txt", Content: &content}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Wrote greet.txt") {
		t.Fatalf("unexpected result %q", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "greet.txt")); err != nil {
		t.Fatalf("file must exist: %v", err)
	}
}
