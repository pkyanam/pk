// Package clipboard provides an explicit, request-scoped native clipboard read.
// It never polls or reads the clipboard unless Read is called by the caller.
package clipboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const MaxFiles = 8
const MaxImageBytes = 5 << 20

type Provider interface {
	Read(context.Context) (Snapshot, error)
}

type Snapshot struct {
	Files   []string
	Image   *Image
	Text    string
	Message string
}

type Image struct {
	ContentType string
	Bytes       []byte
}

type SelectedFile struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	ContentType string `json:"content_type,omitempty"`
}

// Capture reads one explicit clipboard selection, validates selected file URLs,
// and persists any native clipboard image in a private, non-overwriting file.
func Capture(ctx context.Context, provider Provider, artifactDir string) ([]SelectedFile, error) {
	if provider == nil {
		provider = NativeProvider{}
	}
	snapshot, err := provider.Read(ctx)
	if err != nil {
		return nil, err
	}
	return CaptureSnapshot(ctx, snapshot, artifactDir)
}

func CaptureSnapshot(ctx context.Context, snapshot Snapshot, artifactDir string) ([]SelectedFile, error) {
	snapshot = NormalizeSnapshot(snapshot)
	// A file URL selection often has an image preview on NSPasteboard. Prefer
	// the actual files so Finder icons/previews do not become duplicate inputs.
	if len(snapshot.Files) > 0 {
		snapshot.Image = nil
	}
	if len(snapshot.Files) > MaxFiles || (snapshot.Image != nil && len(snapshot.Files) >= MaxFiles) {
		return nil, fmt.Errorf("clipboard selection exceeds the %d file limit", MaxFiles)
	}
	files := make([]SelectedFile, 0, len(snapshot.Files)+1)
	seen := map[string]bool{}
	for _, selected := range snapshot.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.TrimSpace(selected) == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(selected)
		if err != nil {
			return nil, fmt.Errorf("resolve clipboard file %q: %w", selected, err)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("clipboard selection %q is not a readable regular file", selected)
		}
		if info.Size() > 8<<20 {
			return nil, fmt.Errorf("clipboard file %q exceeds the 8 MiB limit", selected)
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		files = append(files, SelectedFile{Path: resolved, Kind: "file"})
	}
	if snapshot.Image != nil {
		path, contentType, err := persistImage(artifactDir, *snapshot.Image)
		if err != nil {
			return nil, err
		}
		files = append(files, SelectedFile{Path: path, Kind: "image", ContentType: contentType})
	}
	if len(files) == 0 {
		if snapshot.Text != "" {
			return files, nil
		}
		return nil, errors.New("clipboard has no supported file or image data")
	}
	return files, nil
}

// NormalizeSnapshot removes plain-text representations when structured file or
// image data is present. Native clipboards commonly expose file names as text
// alongside file URLs; returning both would insert incidental text into prompts.
func NormalizeSnapshot(snapshot Snapshot) Snapshot {
	if len(snapshot.Files) > 0 || snapshot.Image != nil {
		snapshot.Text = ""
	}
	return snapshot
}

func persistImage(dir string, image Image) (string, string, error) {
	if len(image.Bytes) == 0 || len(image.Bytes) > MaxImageBytes {
		return "", "", fmt.Errorf("clipboard image must be between 1 byte and %d bytes", MaxImageBytes)
	}
	ext := ""
	switch image.ContentType {
	case "image/png":
		ext = ".png"
		if len(image.Bytes) < 8 || string(image.Bytes[:8]) != "\x89PNG\r\n\x1a\n" {
			return "", "", errors.New("clipboard PNG data has an invalid signature")
		}
	case "image/tiff":
		ext = ".tiff"
		if len(image.Bytes) < 4 || !(string(image.Bytes[:4]) == "II*\x00" || string(image.Bytes[:4]) == "MM\x00*") {
			return "", "", errors.New("clipboard TIFF data has an invalid signature")
		}
	default:
		return "", "", fmt.Errorf("unsupported clipboard image type %q", image.ContentType)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create clipboard attachment directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("clipboard attachment path must be a real directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("secure clipboard attachment directory: %w", err)
	}
	for range 10 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", "", err
		}
		path := filepath.Join(dir, "clipboard-"+hex.EncodeToString(nonce[:])+ext)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		_, writeErr := file.Write(image.Bytes)
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return "", "", writeErr
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return "", "", closeErr
		}
		return path, image.ContentType, nil
	}
	return "", "", errors.New("could not allocate a unique clipboard image path")
}
