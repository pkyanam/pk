package filetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/workspacejournal"
)

func TestReadIsNotJournaledAndDoesNotMutate(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := workspacejournal.Open(t.TempDir(), workspacejournal.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runJournaledJob(t, workspace, store, request{Action: "Read", Args: args{Path: "file.txt"}})
	if err != nil || !strings.Contains(result, "hello") {
		t.Fatalf("result=%q error=%v", result, err)
	}
	ops, err := store.List(testJournalSession())
	if err != nil || len(ops) != 0 {
		t.Fatalf("Read journaled: %+v %v", ops, err)
	}
	assertContents(t, path, "hello\n")
}
func TestReadArgumentBounds(t *testing.T) {
	for _, n := range []int{0, -1, 2001} {
		if err := validateArgs("Read", args{Path: "a", Limit: &n}); err == nil {
			t.Fatalf("accepted limit %d", n)
		}
	}
	for _, n := range []int{0, -1, 10000001} {
		if err := validateArgs("Read", args{Path: "a", Offset: &n}); err == nil {
			t.Fatalf("accepted offset %d", n)
		}
	}
	if err := validateArgs("Read", args{Path: "a", Content: strptr("write")}); err == nil {
		t.Fatal("accepted mutation arguments")
	}
	n := 1
	if err := validateArgs("WriteFile", args{Path: "a", Content: strptr("write"), Offset: &n}); err == nil {
		t.Fatal("accepted read options in write")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := apply(ctx, t.TempDir(), request{Action: "Read", Args: args{Path: "a"}}); err == nil {
		t.Fatal("ignored cancellation")
	}
}
