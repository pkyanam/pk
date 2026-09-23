package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/runner"
)

func TestAttachmentPromptDirectoryCleanupIsScopedToOneInput(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	options := &runner.Options{SessionDir: sessions}
	_, pageDir, cleanup, err := prepareAttachmentIdentity(options)
	if err != nil {
		t.Fatal(err)
	}
	if !runnerID(options.PreallocatedNewID) {
		t.Fatalf("fresh session ID was not preallocated: %q", options.PreallocatedNewID)
	}
	prior := filepath.Join(attachments.SessionPDFPageDir(sessions, options.PreallocatedNewID), "prior-prompt")
	if err := os.MkdirAll(prior, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prior, "page.png"), []byte("prior"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(pageDir); !os.IsNotExist(err) {
		t.Fatalf("current prompt directory remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(prior, "page.png")); err != nil || string(data) != "prior" {
		t.Fatalf("cleanup touched prior prompt file %q, err=%v", data, err)
	}
}

func TestAttachmentPromptDirectoryRejectsSymlinkedArtifactParent(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(sessions, "pdf-pages")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ensurePrivatePageDir(sessions, filepath.Join(sessions, "pdf-pages", "session", "prompt")); err == nil {
		t.Fatal("accepted symlinked rendered-page parent")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("wrote through symlink: entries=%v err=%v", entries, err)
	}
}

func TestFailedFreshAttachmentSetupRemovesEmptySessionArtifactRoots(t *testing.T) {
	sessions := filepath.Join(t.TempDir(), "sessions")
	options := &runner.Options{SessionDir: sessions}
	_, _, cleanup, err := prepareAttachmentIdentity(options)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(sessions, "pdf-pages")); !os.IsNotExist(err) {
		t.Fatalf("empty PDF page root remains after failed setup: %v", err)
	}
}

func runnerID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
