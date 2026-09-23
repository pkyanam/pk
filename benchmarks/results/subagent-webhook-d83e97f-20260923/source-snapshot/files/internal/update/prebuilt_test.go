package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStageLatestPrebuiltVerifiesAndImportsPairedRelease(t *testing.T) {
	archive := testReleaseArchive(t, false)
	sum := sha256.Sum256(archive)
	assetName := "pk_darwin_arm64.tar.gz"
	checksums := fmt.Sprintf("%x  %s\n", sum, assetName)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/releases/latest":
			_, _ = fmt.Fprint(w, `{"tag_name":"v1.2.3","assets":[{"name":"pk_darwin_arm64.tar.gz"},{"name":"SHA256SUMS"}]}`)
		case "/releases/download/v1.2.3/SHA256SUMS":
			_, _ = io.WriteString(w, checksums)
		case "/releases/download/v1.2.3/" + assetName:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := Manager{Root: filepath.Join(t.TempDir(), "pk"), ReleaseAPIBaseURL: server.URL, ReleaseDownloadBaseURL: server.URL, HTTPClient: server.Client()}
	t.Cleanup(func() { makeTreeWritable(t, manager.Root) })
	release, alreadyCurrent, err := manager.StageLatestPrebuilt(context.Background(), "darwin", "arm64")
	if err != nil || alreadyCurrent {
		t.Fatalf("stage prebuilt = (%+v, %v, %v)", release, alreadyCurrent, err)
	}
	if release.GitRef != "v1.2.3" || release.Revision != "v1.2.3" || release.GitRepository != "https://github.com/pkyanam/pk.git" || release.DistributionSHA256 != hex.EncodeToString(sum[:]) || release.SourceHash != "" {
		t.Fatalf("release provenance=%+v", release)
	}
	if err := validateRelease(release); err != nil {
		t.Fatalf("imported release invalid: %v", err)
	}
	if _, err := os.Stat(filepath.Join(release.Path, "ui", "node_modules", ".bin", "tsc")); err != nil {
		t.Fatalf("paired UI symlink missing: %v", err)
	}
	requestsAfterStage := requests
	if err := manager.Activate(context.Background(), release.ID, func(context.Context, Release) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, alreadyCurrent, err := manager.StageLatestPrebuilt(context.Background(), "darwin", "arm64")
	if err != nil || !alreadyCurrent || got.ID != release.ID {
		t.Fatalf("matching active release = (%+v,%v,%v)", got, alreadyCurrent, err)
	}
	if requests != requestsAfterStage+1 {
		t.Fatalf("same-version update downloaded assets: requests %d -> %d", requestsAfterStage, requests)
	}
}

func TestStageLatestPrebuiltFailsClosedOnChecksumAndArchiveErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		archive   []byte
		checksums string
		want      string
	}{
		{name: "checksum mismatch", archive: testReleaseArchive(t, false), checksums: strings.Repeat("0", 64) + "  pk_linux_amd64.tar.gz\n", want: "checksum mismatch"},
		{name: "path traversal", archive: testReleaseArchive(t, true), want: "traversal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := test.archive
			checksums := test.checksums
			if checksums == "" {
				sum := sha256.Sum256(archive)
				checksums = fmt.Sprintf("%x  pk_linux_amd64.tar.gz\n", sum)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/releases/latest":
					_, _ = fmt.Fprint(w, `{"tag_name":"v1.2.3","assets":[{"name":"pk_linux_amd64.tar.gz"}]}`)
				case "/releases/download/v1.2.3/SHA256SUMS":
					_, _ = io.WriteString(w, checksums)
				case "/releases/download/v1.2.3/pk_linux_amd64.tar.gz":
					_, _ = w.Write(archive)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			manager := Manager{Root: filepath.Join(t.TempDir(), "pk"), ReleaseAPIBaseURL: server.URL, ReleaseDownloadBaseURL: server.URL, HTTPClient: server.Client()}
			_, _, err := manager.StageLatestPrebuilt(context.Background(), "linux", "amd64")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("StageLatestPrebuilt error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestStageLatestPrebuiltDistinguishesMissingPlatformFromNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"tag_name":"v1.2.3","assets":[{"name":"pk_linux_amd64.tar.gz"}]}`)
	}))
	manager := Manager{Root: filepath.Join(t.TempDir(), "pk"), ReleaseAPIBaseURL: server.URL, ReleaseDownloadBaseURL: server.URL, HTTPClient: server.Client()}
	_, _, err := manager.StageLatestPrebuilt(context.Background(), "darwin", "arm64")
	server.Close()
	if !errors.Is(err, ErrNoPlatformRelease) {
		t.Fatalf("missing platform error=%v", err)
	}

	manager = Manager{Root: filepath.Join(t.TempDir(), "pk"), ReleaseAPIBaseURL: "http://127.0.0.1:1", HTTPClient: &http.Client{Timeout: 50 * time.Millisecond}}
	_, _, err = manager.StageLatestPrebuilt(context.Background(), "linux", "amd64")
	if err == nil || errors.Is(err, ErrNoPublishedRelease) || errors.Is(err, ErrNoPlatformRelease) {
		t.Fatalf("network failure was misclassified for source fallback: %v", err)
	}
}

func testReleaseArchive(t *testing.T, traversal bool) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	writer := tar.NewWriter(gz)
	entries := []struct {
		name string
		kind byte
		mode int64
		link string
		body string
	}{
		{name: "pk", kind: tar.TypeReg, mode: 0o755, body: "#!/bin/sh\nexit 0\n"},
		{name: "ui", kind: tar.TypeDir, mode: 0o755},
		{name: "ui/package.json", kind: tar.TypeReg, mode: 0o644, body: `{"type":"module"}`},
		{name: "ui/bun.lock", kind: tar.TypeReg, mode: 0o644, body: "locked"},
		{name: "ui/dist", kind: tar.TypeDir, mode: 0o755},
		{name: "ui/dist/main.js", kind: tar.TypeReg, mode: 0o644, body: "export {}"},
		{name: "ui/node_modules", kind: tar.TypeDir, mode: 0o755},
		{name: "ui/node_modules/typescript", kind: tar.TypeDir, mode: 0o755},
		{name: "ui/node_modules/typescript/bin", kind: tar.TypeDir, mode: 0o755},
		{name: "ui/node_modules/typescript/bin/tsc", kind: tar.TypeReg, mode: 0o755, body: "#!/bin/sh\nexit 0\n"},
		{name: "ui/node_modules/.bin", kind: tar.TypeDir, mode: 0o755},
		{name: "ui/node_modules/.bin/tsc", kind: tar.TypeSymlink, mode: 0o777, link: "../typescript/bin/tsc"},
	}
	if traversal {
		entries = append(entries, struct {
			name string
			kind byte
			mode int64
			link string
			body string
		}{name: "../escape", kind: tar.TypeReg, mode: 0o644, body: "bad"})
	}
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: entry.mode, Linkname: entry.link, Size: int64(len(entry.body))}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := io.WriteString(writer, entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
