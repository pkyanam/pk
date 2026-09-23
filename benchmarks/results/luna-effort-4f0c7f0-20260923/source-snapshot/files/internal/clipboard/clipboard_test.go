package clipboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeProvider struct{ snapshot Snapshot }

func (f fakeProvider) Read(context.Context) (Snapshot, error) { return f.snapshot, nil }

func TestCaptureStoresSelectedImagePrivatelyWithoutOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attachments")
	selected, err := Capture(context.Background(), fakeProvider{snapshot: Snapshot{Text: "see image", Image: &Image{
		ContentType: "image/png", Bytes: []byte("\x89PNG\r\n\x1a\nfixture"),
	}}}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Kind != "image" || selected[0].ContentType != "image/png" {
		t.Fatalf("selection = %#v", selected)
	}
	info, err := os.Stat(selected[0].Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("image permissions/stat = %v, %v", info, err)
	}
	if filepath.Dir(selected[0].Path) != dir {
		t.Fatalf("image path %q outside managed artifact dir %q", selected[0].Path, dir)
	}
	got, err := os.ReadFile(selected[0].Path)
	if err != nil || string(got) != "\x89PNG\r\n\x1a\nfixture" {
		t.Fatalf("image data = %q, err=%v", got, err)
	}
	second, err := Capture(context.Background(), fakeProvider{snapshot: Snapshot{Image: &Image{
		ContentType: "image/png", Bytes: []byte("\x89PNG\r\n\x1a\nfixture"),
	}}}, dir)
	if err != nil || second[0].Path == selected[0].Path {
		t.Fatalf("second selection=%#v err=%v; image paths must not overwrite", second, err)
	}
}

func TestCaptureValidatesExplicitFilesAndLimits(t *testing.T) {
	temp := t.TempDir()
	file := filepath.Join(temp, "chosen file.txt")
	if err := os.WriteFile(file, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := CaptureSnapshot(context.Background(), Snapshot{Files: []string{file}, Text: "text"}, filepath.Join(temp, "managed"))
	if err != nil || len(got) != 1 || filepath.Base(got[0].Path) != filepath.Base(file) {
		t.Fatalf("selection=%#v err=%v", got, err)
	}
	if _, err := CaptureSnapshot(context.Background(), Snapshot{Files: []string{temp}}, filepath.Join(temp, "managed")); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory selection error=%v", err)
	}
	tooMany := make([]string, MaxFiles+1)
	if _, err := CaptureSnapshot(context.Background(), Snapshot{Files: tooMany}, filepath.Join(temp, "managed")); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("file limit error=%v", err)
	}
	if _, err := CaptureSnapshot(context.Background(), Snapshot{Image: &Image{ContentType: "image/png", Bytes: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxImageBytes)...)}}, filepath.Join(temp, "managed")); err == nil || !strings.Contains(err.Error(), "5242880") {
		t.Fatalf("image limit error=%v", err)
	}
}

func TestCaptureAllowsTextOnlySnapshot(t *testing.T) {
	got, err := CaptureSnapshot(context.Background(), Snapshot{Text: "ordinary prose"}, t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("selection=%#v err=%v", got, err)
	}
}

func TestCapturePrefersClipboardFileURLsOverPreviewImage(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "selected.png")
	if err := os.WriteFile(file, []byte("user-selected-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(dir, "managed")
	got, err := CaptureSnapshot(context.Background(), Snapshot{
		Files: []string{file}, Text: "selected.png", Image: &Image{ContentType: "image/png", Bytes: []byte("\x89PNG\r\n\x1a\npreview")},
	}, managed)
	if err != nil || len(got) != 1 || filepath.Base(got[0].Path) != filepath.Base(file) {
		t.Fatalf("selection=%#v err=%v", got, err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("preview image was persisted despite selected file URLs: stat err=%v", err)
	}
}

func TestNormalizeSnapshotSuppressesIncidentalTextWithAttachments(t *testing.T) {
	for name, snapshot := range map[string]Snapshot{
		"files": {Files: []string{"/tmp/a.txt"}, Text: "a.txt"},
		"image": {Image: &Image{ContentType: "image/png", Bytes: []byte("bytes")}, Text: "image.png"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := NormalizeSnapshot(snapshot); got.Text != "" {
				t.Fatalf("incidental clipboard text retained: %q", got.Text)
			}
		})
	}
	if got := NormalizeSnapshot(Snapshot{Text: "plain text"}); got.Text != "plain text" {
		t.Fatalf("plain text lost: %q", got.Text)
	}
}
