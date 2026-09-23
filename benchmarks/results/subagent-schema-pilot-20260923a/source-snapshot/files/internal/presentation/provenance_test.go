package presentation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/attachments"
)

func TestSidecarStoresOnlyBoundedChipsAndMatchesExactPrompt(t *testing.T) {
	dir := t.TempDir()
	userPrompt := "Review résumé"
	effective := userPrompt + "\n\n" + strings.Repeat("private extracted attachment contents", 100)
	record, err := NewRecord(userPrompt, effective, []attachments.Attachment{{Path: "/Users/test/Downloads/report résumé.txt", Kind: attachments.Text, ContentType: "text/plain", Text: "private extracted attachment contents"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, "session/with/path", "input/with/path", record); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, "session/with/path", "input/with/path")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Matches(effective) || loaded.PrefixBytes != len(userPrompt) || len(loaded.Attachments) != 1 || loaded.Attachments[0].Name != "report résumé.txt" {
		t.Fatalf("loaded provenance=%+v", loaded)
	}
	if loaded.Matches(userPrompt+"\n\nchanged suffix") || loaded.Matches("changed prompt"+effective[len(userPrompt):]) {
		t.Fatal("accepted a changed effective payload or prompt prefix")
	}
	data, err := os.ReadFile(RecordPath(dir, "session/with/path", "input/with/path"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private extracted attachment contents") || strings.Contains(string(data), "/Users/test") {
		t.Fatalf("sidecar leaked file content or absolute path: %s", data)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"effective_payload_sha256", "prompt_prefix_sha256", "prefix_bytes", "attachments"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing metadata field %q: %s", key, data)
		}
	}
	info, err := os.Stat(filepath.Dir(RecordPath(dir, "session/with/path", "input/with/path")))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("sidecar directory mode=%v err=%v", info, err)
	}
	if info, err := os.Stat(RecordPath(dir, "session/with/path", "input/with/path")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode=%v err=%v", info, err)
	}
}

func TestProvenanceRejectsInvalidBoundsAndCorruptSidecars(t *testing.T) {
	_, err := NewRecord("a", "a note", []attachments.Attachment{{Path: "a.txt", Kind: attachments.Text}, {Path: "b.txt", Kind: attachments.Text}, {Path: "c.txt", Kind: attachments.Text}, {Path: "d.txt", Kind: attachments.Text}, {Path: "e.txt", Kind: attachments.Text}, {Path: "f.txt", Kind: attachments.Text}, {Path: "g.txt", Kind: attachments.Text}, {Path: "h.txt", Kind: attachments.Text}, {Path: "i.txt", Kind: attachments.Text}})
	if err == nil {
		t.Fatal("accepted too many attachment chips")
	}
	dir := t.TempDir()
	path := RecordPath(dir, "session", "input")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"prefix_bytes":0,"prompt_prefix_sha256":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, "session", "input"); err == nil {
		t.Fatal("accepted corrupt metadata")
	}
}

func TestMatchesRejectsSplitUTF8Prefix(t *testing.T) {
	record, err := NewRecord("é", "é + note", []attachments.Attachment{{Path: "é.txt", Kind: attachments.Image}})
	if err != nil {
		t.Fatal(err)
	}
	record.PrefixBytes = 1 // inside the two-byte encoding of é
	if record.Matches("é + note") {
		t.Fatal("accepted a prefix ending inside a UTF-8 rune")
	}
}
