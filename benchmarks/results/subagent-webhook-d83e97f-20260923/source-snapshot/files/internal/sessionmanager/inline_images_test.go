package sessionmanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/attachments"
)

func TestInlineImagesFollowSessionArchiveRestoreAndPurge(t *testing.T) {
	manager, sessionDir := fixtureManager(t)
	const id = "session-inline-images"
	if _, err := createSession(t, sessionDir, id, "Inspect image", "Image inspected"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(attachments.PromptImageDir(sessionDir, id, "input-image"), "image-1.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const contents = "private image fixture"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	archived := manager.Archive(ctx, []string{id}, nil)
	if len(archived) != 1 || !archived[0].OK {
		t.Fatalf("archive: %+v", archived)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("image left in live store: %v", err)
	}
	restored := manager.Restore(ctx, []string{archived[0].TrashID}, nil)
	if len(restored) != 1 || !restored[0].OK {
		t.Fatalf("restore: %+v", restored)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != contents {
		t.Fatalf("restored image mismatch: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored image permissions: %v, %v", info, err)
	}
	archived = manager.Archive(ctx, []string{id}, nil)
	if len(archived) != 1 || !archived[0].OK {
		t.Fatalf("second archive: %+v", archived)
	}
	purged := manager.Purge(ctx, []string{archived[0].TrashID}, nil)
	if len(purged) != 1 || !purged[0].OK {
		t.Fatalf("purge: %+v", purged)
	}
	trashPath := filepath.Join(filepath.Dir(sessionDir), "trash", "sessions", archived[0].TrashID)
	for _, removed := range []string{path, trashPath} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("artifact remains after purge: %s (%v)", removed, err)
		}
	}
}
