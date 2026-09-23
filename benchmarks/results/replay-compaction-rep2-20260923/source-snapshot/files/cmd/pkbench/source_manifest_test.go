package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSourceSnapshotCopiesTextAndVerifiesManifestHash(t *testing.T) {
	repo, output := t.TempDir(), t.TempDir()
	const relative = "cmd/pkbench/main.go"
	content := []byte("package main\n// reproducible source\n")
	source := filepath.Join(repo, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	manifest := sourceManifest{Files: []sourceFileDigest{{Path: relative, SHA256: hex.EncodeToString(digest[:])}}}
	if err := writeSourceSnapshot(repo, output, manifest); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(output, "source-snapshot", "files", filepath.FromSlash(relative))
	got, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("snapshot content = %q, want %q", got, content)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestWriteSourceSnapshotRejectsHashMismatchAndBinaryData(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
		digest  string
	}{
		{name: "hash mismatch", content: []byte("changed"), digest: "incorrect"},
		{name: "binary", content: []byte{0xff, 0x00}, digest: func() string { h := sha256.Sum256([]byte{0xff, 0x00}); return hex.EncodeToString(h[:]) }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, output := t.TempDir(), t.TempDir()
			const relative = "src/file.go"
			source := filepath.Join(repo, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			manifest := sourceManifest{Files: []sourceFileDigest{{Path: relative, SHA256: test.digest}}}
			if err := writeSourceSnapshot(repo, output, manifest); err == nil {
				t.Fatal("expected source snapshot validation error")
			}
		})
	}
}
