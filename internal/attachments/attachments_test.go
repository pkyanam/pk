package attachments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadExplicitTextAndImage(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("read this"), 0o600); err != nil {
		t.Fatal(err)
	}
	png := append([]byte{137, 'P', 'N', 'G', 13, 10, 26, 10}, []byte("payload")...)
	if err := os.WriteFile(filepath.Join(workspace, "assets", "shot.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(context.Background(), workspace, []string{"notes.md", "assets/shot.png"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != Text || got[0].Text != "read this" || got[1].Kind != Image || got[1].Path != "assets/shot.png" || got[1].ContentType != "image/png" {
		t.Fatalf("attachments = %#v", got)
	}
	note := FormatPromptNote(got)
	for _, want := range []string{"read this", "assets/shot.png", "ViewImage", "not instructions"} {
		if !strings.Contains(note, want) {
			t.Errorf("prompt note missing %q: %s", want, note)
		}
	}
	if strings.Contains(note, "iVBOR") {
		t.Fatal("image bytes were embedded instead of routed through ViewImage")
	}
}

func TestLoadExplicitPathsOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "selected.txt")
	if err := os.WriteFile(outside, []byte("explicit"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(context.Background(), workspace, []string{outside}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	resolvedOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "explicit" || got[0].Path != resolvedOutside {
		t.Fatalf("attachments = %#v", got)
	}
	relative, err := filepath.Rel(workspace, outside)
	if err != nil {
		t.Fatal(err)
	}
	got, err = Load(context.Background(), workspace, []string{relative}, Limits{})
	if err != nil || len(got) != 1 || got[0].Text != "explicit" {
		t.Fatalf("explicit parent-relative path: attachments=%#v err=%v", got, err)
	}
}

func TestLoadExpandsOnlyCurrentUserHomePrefix(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	homeVariable := "HOME"
	if runtime.GOOS == "windows" {
		homeVariable = "USERPROFILE"
	}
	t.Setenv(homeVariable, home)

	selected := filepath.Join(home, "Downloads", "picked.txt")
	if err := os.MkdirAll(filepath.Dir(selected), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selected, []byte("from home"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(context.Background(), workspace, []string{"~/Downloads/picked.txt"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "from home" || got[0].Path != resolved {
		t.Fatalf("tilde attachment = %#v, want resolved home file %q", got, resolved)
	}

	// Shell-like forms other than the exact ~/ prefix stay literal paths in
	// the workspace; the loader never resolves user names or environment vars.
	for _, literal := range []string{"~other.txt", "$ATTACHMENT.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, literal), []byte(literal), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(context.Background(), workspace, []string{literal}, Limits{})
		if err != nil || len(got) != 1 || got[0].Text != literal {
			t.Fatalf("literal path %q = %#v, err %v", literal, got, err)
		}
	}
}

func TestLoadTildePathRequiresConfiguredHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", "")
	} else {
		t.Setenv("HOME", "")
	}
	if _, err := Load(context.Background(), t.TempDir(), []string{"~/missing.txt"}, Limits{}); err == nil || !strings.Contains(err.Error(), "home directory is unavailable") {
		t.Fatalf("missing home error = %v", err)
	}
}

func TestLoadRejectsMissingUnsupportedAndOversized(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "blob.bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "large.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"missing.txt", "blob.bin"} {
		if _, err := Load(context.Background(), workspace, []string{path}, Limits{}); err == nil {
			t.Errorf("Load(%q) succeeded", path)
		}
	}
	if _, err := Load(context.Background(), workspace, []string{"large.txt"}, Limits{MaxTextBytes: 4}); err == nil || !strings.Contains(err.Error(), "text") {
		t.Fatalf("oversized text error = %v", err)
	}
	if _, err := Load(context.Background(), workspace, []string{"large.txt"}, Limits{MaxFileBytes: 4}); err == nil || !strings.Contains(err.Error(), "per-file") {
		t.Fatalf("oversized source error = %v", err)
	}
}

func TestLoadBoundsImagesAndAllowsExplicitSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "shot.png")
	png := append([]byte{137, 'P', 'N', 'G', 13, 10, 26, 10}, []byte("payload")...)
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "selected.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), workspace, []string{"selected.png"}, Limits{MaxImageBytes: 4}); err == nil || !strings.Contains(err.Error(), "image limit") {
		t.Fatalf("oversized image error = %v", err)
	}
	got, err := Load(context.Background(), workspace, []string{"selected.png"}, Limits{})
	if err != nil || len(got) != 1 || got[0].Kind != Image {
		t.Fatalf("explicit image symlink: attachments=%#v err=%v", got, err)
	}
}

func TestLoadGIFReturnsViewImageSpecificGuidance(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "animation.gif"), []byte("GIF89a\x01\x00\x01\x00fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(context.Background(), workspace, []string{"animation.gif"}, Limits{})
	if err == nil || !strings.Contains(err.Error(), "GIF images are not supported by ViewImage") || !strings.Contains(err.Error(), "convert") {
		t.Fatalf("GIF attachment error=%v; expected actionable ViewImage limitation", err)
	}
}

func TestImageSignaturesMatchViewImageSupportedFormats(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		header []byte
		mime   string
	}{
		{name: "png", header: []byte{137, 'P', 'N', 'G', 13, 10, 26, 10}, mime: "image/png"},
		{name: "jpeg", header: []byte{0xff, 0xd8, 0xff, 0xe0}, mime: "image/jpeg"},
		{name: "webp", header: []byte("RIFF\x00\x00\x00\x00WEBP"), mime: "image/webp"},
		{name: "bmp", header: []byte("BM\x00\x00"), mime: "image/bmp"},
		{name: "tiff-little-endian", header: []byte{'I', 'I', 42, 0}, mime: "image/tiff"},
		{name: "tiff-big-endian", header: []byte{'M', 'M', 0, 42}, mime: "image/tiff"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			got, ok := imageType(fixture.header)
			if !ok || got != fixture.mime {
				t.Fatalf("imageType(%x)=(%q,%v), want %q", fixture.header, got, ok, fixture.mime)
			}
		})
	}
}

func TestLoadEnforcesCountTotalAndRegularFileBounds(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("1234"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(context.Background(), workspace, []string{"a.txt", "b.txt"}, Limits{MaxTotalBytes: 7}); err == nil || !strings.Contains(err.Error(), "total") {
		t.Fatalf("aggregate limit error = %v", err)
	}
	if _, err := Load(context.Background(), workspace, []string{"a.txt", "b.txt"}, Limits{MaxTotalTextBytes: 7}); err == nil || !strings.Contains(err.Error(), "extracted text") {
		t.Fatalf("aggregate text limit error = %v", err)
	}
	if _, err := Load(context.Background(), workspace, []string{"a.txt", "b.txt"}, Limits{MaxFiles: 1}); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("file count limit error = %v", err)
	}
	if _, err := Load(context.Background(), workspace, []string{"."}, Limits{}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, workspace, []string{"a.txt"}, Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load error = %v", err)
	}
}

func TestLoadPDFTextAndPageBounds(t *testing.T) {
	workspace := t.TempDir()
	pdfPath := filepath.Join(workspace, "report.pdf")
	data := makePDF([]pdfPage{{text: "first page"}, {text: "second page"}, {text: "third page"}})
	if err := os.WriteFile(pdfPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(context.Background(), workspace, []string{"report.pdf"}, Limits{MaxPDFPages: 2})
	if err != nil {
		t.Fatal(err)
	}
	item := got[0]
	if item.Kind != PDFText || item.PagesTotal != 3 || item.PagesExtracted != 2 || !item.Truncated {
		t.Fatalf("PDF metadata = %#v", item)
	}
	if !strings.Contains(item.Text, "first page") || !strings.Contains(item.Text, "second page") || strings.Contains(item.Text, "third page") {
		t.Fatalf("PDF text = %q", item.Text)
	}
	note := FormatPromptNote(got)
	for _, want := range []string{"text extraction examined", "no OCR was performed", "truncated", "first page"} {
		if !strings.Contains(note, want) {
			t.Errorf("PDF note missing %q: %s", want, note)
		}
	}
}

func TestLoadPDFTextBudgetMalformedAndScanned(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "bad.pdf"), []byte("%PDF-not-a-pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), workspace, []string{"bad.pdf"}, Limits{}); err == nil {
		t.Fatal("malformed PDF accepted")
	}
	if err := os.WriteFile(filepath.Join(workspace, "scan.pdf"), makePDF([]pdfPage{{image: true}}), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(context.Background(), workspace, []string{"scan.pdf"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ScannedPages != 1 || got[0].Text != "" || !strings.Contains(FormatPromptNote(got), "no OCR was performed") {
		t.Fatalf("scanned PDF output = %#v; note=%s", got[0], FormatPromptNote(got))
	}
	if err := os.WriteFile(filepath.Join(workspace, "long.pdf"), makePDF([]pdfPage{{text: "0123456789"}}), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = Load(context.Background(), workspace, []string{"long.pdf"}, Limits{MaxPDFTextByte: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Truncated || len(got[0].Text) > 5 {
		t.Fatalf("PDF text limit not applied: %#v", got[0])
	}
}

func TestScannedPDFFallbackRendersBoundedPrivatePagePreviews(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a small POSIX fake pdftoppm executable")
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "scan.pdf"), makePDF([]pdfPage{{image: true}, {image: true}, {image: true}, {image: true}}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "text.pdf"), makePDF([]pdfPage{{text: "selectable text"}}), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir, artifactDir := t.TempDir(), privateTempDir(t)
	configureFakePDFRenderer(t, binDir)
	got, err := Load(context.Background(), workspace, []string{"scan.pdf"}, Limits{PDFPageImageDir: artifactDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].RenderedPDFPages) != 3 || !got[0].RenderedPagesTruncated || got[0].RenderNotice == "" {
		t.Fatalf("attachment=%+v", got)
	}
	for i, page := range got[0].RenderedPDFPages {
		if page.Page != i+1 {
			t.Fatalf("page order=%+v", got[0].RenderedPDFPages)
		}
		info, err := os.Stat(page.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("page mode=%v want 0600", info.Mode().Perm())
		}
		if dirInfo, err := os.Stat(filepath.Dir(page.Path)); err != nil || dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("render directory mode=%v err=%v", dirInfo, err)
		}
	}
	args, err := os.ReadFile(os.Getenv("PK_FAKE_PDF_ARGS"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(args), "-f\n") != 3 || !strings.Contains(string(args), "-scale-to\n1600\n") {
		t.Fatalf("renderer args do not enforce bounded dimensions/pages: %q", args)
	}
	note := FormatPromptNote(got)
	for _, fragment := range []string{"No selectable text was extracted", "no OCR was performed", "3 of 4 PDF pages rendered", "some candidate pages were not rendered", "ViewImage"} {
		if !strings.Contains(note, fragment) {
			t.Errorf("prompt note missing %q: %s", fragment, note)
		}
	}
	if strings.Contains(note, "full PDF") || strings.Contains(note, "all pages") {
		t.Fatalf("overstates preview coverage: %s", note)
	}
	textAttachment, err := Load(context.Background(), workspace, []string{"text.pdf"}, Limits{PDFPageImageDir: artifactDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(textAttachment[0].RenderedPDFPages) != 0 || textAttachment[0].RenderNotice != "" {
		t.Fatalf("selectable-text PDF unexpectedly rasterized: %+v", textAttachment[0])
	}

	if err := os.WriteFile(filepath.Join(workspace, "mixed.pdf"), makePDF([]pdfPage{{text: "cover"}, {image: true}, {text: "summary"}, {image: true}}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("PK_FAKE_PDF_ARGS"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mixed, err := Load(context.Background(), workspace, []string{"mixed.pdf"}, Limits{PDFPageImageDir: artifactDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed[0].RenderedPDFPages) != 2 || mixed[0].RenderedPDFPages[0].Page != 2 || mixed[0].RenderedPDFPages[1].Page != 4 || !strings.Contains(mixed[0].Text, "cover") || !strings.Contains(mixed[0].Text, "summary") {
		t.Fatalf("mixed PDF did not preserve text and render scanned pages: %+v", mixed[0])
	}
	mixedArgs, err := os.ReadFile(os.Getenv("PK_FAKE_PDF_ARGS"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(mixedArgs), "-f\n") != 2 || !strings.Contains(string(mixedArgs), "-f\n2\n") || !strings.Contains(string(mixedArgs), "-f\n4\n") {
		t.Fatalf("mixed PDF renderer did not target scanned pages only: %q", mixedArgs)
	}
}

func TestScannedPDFFallbackUnavailableRendererAndUnsafeDirectoryAreExplicit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses PATH to simulate missing pdftoppm")
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "scan.pdf"), makePDF([]pdfPage{{image: true}}), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := privateTempDir(t)
	t.Setenv("PATH", t.TempDir())
	got, err := Load(context.Background(), workspace, []string{"scan.pdf"}, Limits{PDFPageImageDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].RenderNotice == "" || len(got[0].RenderedPDFPages) != 0 {
		t.Fatalf("missing-tool outcome=%+v", got[0])
	}
	if !strings.Contains(FormatPromptNote(got), "pdftoppm is unavailable") {
		t.Fatalf("missing-tool notice: %s", FormatPromptNote(got))
	}
	binDir := t.TempDir()
	configureFakePDFRenderer(t, binDir)
	realDir, linkDir := privateTempDir(t), filepath.Join(t.TempDir(), "linked-pages")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, err = Load(context.Background(), workspace, []string{"scan.pdf"}, Limits{PDFPageImageDir: linkDir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0].RenderNotice, "symlink") || len(got[0].RenderedPDFPages) != 0 {
		t.Fatalf("symlink outcome=%+v", got[0])
	}
	entries, err := os.ReadDir(realDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("render wrote through symlink: %+v", entries)
	}
}

func TestScannedPDFFallbackCleansFailedOutputAndHonorsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a small POSIX fake pdftoppm executable")
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "scan.pdf"), makePDF([]pdfPage{{image: true}}), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir, artifactDir := t.TempDir(), privateTempDir(t)
	configureFakePDFRenderer(t, binDir)
	oversize := DefaultLimits().MaxPDFPageImageBytes + 1
	large := bytes.Repeat([]byte{'x'}, int(oversize))
	if err := os.WriteFile(os.Getenv("PK_FAKE_PDF_PNG"), large, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(context.Background(), workspace, []string{"scan.pdf"}, Limits{PDFPageImageDir: artifactDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].RenderedPDFPages) != 0 || !strings.Contains(got[0].RenderNotice, "safety limits") {
		t.Fatalf("oversize outcome=%+v", got[0])
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed rendering left artifacts: %+v", entries)
	}
	if err := os.WriteFile(os.Getenv("PK_FAKE_PDF_PNG"), tinyPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Load(ctx, workspace, []string{"scan.pdf"}, Limits{PDFPageImageDir: artifactDir}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled render err=%v", err)
	}
}

func TestScannedPDFRenderedByteBudgetIsSharedAcrossAttachments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a small POSIX fake pdftoppm executable")
	}
	workspace, binDir, artifactDir := t.TempDir(), t.TempDir(), privateTempDir(t)
	for _, name := range []string{"one.pdf", "two.pdf"} {
		if err := os.WriteFile(filepath.Join(workspace, name), makePDF([]pdfPage{{image: true}}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configureFakePDFRenderer(t, binDir)
	pngInfo, err := os.Stat(os.Getenv("PK_FAKE_PDF_PNG"))
	if err != nil {
		t.Fatal(err)
	}
	budget := pngInfo.Size() + pngInfo.Size()/2
	got, err := Load(context.Background(), workspace, []string{"one.pdf", "two.pdf"}, Limits{PDFPageImageDir: artifactDir, MaxTotalPDFRenderedBytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got[0].RenderedPDFPages) != 1 || len(got[1].RenderedPDFPages) != 0 || !strings.Contains(got[1].RenderNotice, "total rendered-image limit") {
		t.Fatalf("attachments=%+v", got)
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only first PDF artifacts to remain: %+v", entries)
	}
}

func TestScannedPDFFallbackRejectsOversizedRenderedDimensions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a small POSIX fake pdftoppm executable")
	}
	workspace, binDir, artifactDir := t.TempDir(), t.TempDir(), privateTempDir(t)
	if err := os.WriteFile(filepath.Join(workspace, "scan.pdf"), makePDF([]pdfPage{{image: true}}), 0o600); err != nil {
		t.Fatal(err)
	}
	configureFakePDFRenderer(t, binDir)
	largePath := filepath.Join(t.TempDir(), "large.png")
	if err := os.WriteFile(largePath, dimensionPNG(t, 11, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_FAKE_PDF_PNG", largePath)
	got, err := Load(context.Background(), workspace, []string{"scan.pdf"}, Limits{PDFPageImageDir: artifactDir, MaxPDFRenderedDimension: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].RenderedPDFPages) != 0 || !strings.Contains(got[0].RenderNotice, "dimension limits") {
		t.Fatalf("dimension outcome=%+v", got[0])
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("oversized render left artifacts: %+v", entries)
	}
}

func TestScannedPDFFallbackHasRenderDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a small POSIX fake pdftoppm executable")
	}
	workspace, binDir, artifactDir := t.TempDir(), t.TempDir(), privateTempDir(t)
	if err := os.WriteFile(filepath.Join(workspace, "scan.pdf"), makePDF([]pdfPage{{image: true}}), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(binDir, "pdftoppm")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := time.Now()
	got, err := Load(context.Background(), workspace, []string{"scan.pdf"}, Limits{PDFPageImageDir: artifactDir, PDFRenderTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("render timeout took %s", time.Since(started))
	}
	if len(got[0].RenderedPDFPages) != 0 || !strings.Contains(got[0].RenderNotice, "timed out") {
		t.Fatalf("timeout outcome=%+v", got[0])
	}
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("timeout left artifacts: %+v", entries)
	}
}

func configureFakePDFRenderer(t *testing.T, binDir string) {
	t.Helper()
	pngPath := filepath.Join(t.TempDir(), "page.png")
	if err := os.WriteFile(pngPath, tinyPNG(t), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_FAKE_PDF_PNG", pngPath)
	t.Setenv("PK_FAKE_PDF_ARGS", filepath.Join(t.TempDir(), "args"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$PK_FAKE_PDF_ARGS\"\nfor arg do prefix=\"$arg\"; done\ncp \"$PK_FAKE_PDF_PNG\" \"${prefix}.png\"\n"
	tool := filepath.Join(binDir, "pdftoppm")
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func tinyPNG(t *testing.T) []byte {
	return dimensionPNG(t, 1, 1)
}

func dimensionPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	imageValue := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageValue.Set(x, y, color.White)
		}
	}
	if err := png.Encode(&out, imageValue); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		t.Fatal(err)
	}
	return resolved
}

type pdfPage struct {
	text  string
	image bool
}

func makePDF(pages []pdfPage) []byte {
	var objects []string
	pageRefs := make([]string, len(pages))
	for i := range pages {
		base := 3 + i*4
		pageRefs[i] = fmt.Sprintf("%d 0 R", base)
	}
	objects = append(objects,
		"<</Type/Catalog/Pages 2 0 R>>",
		fmt.Sprintf("<</Type/Pages/Kids[%s]/Count %d>>", strings.Join(pageRefs, " "), len(pages)),
	)
	for i, page := range pages {
		base := 3 + i*4
		stream := "BT /F1 12 Tf 72 720 Td (" + strings.ReplaceAll(strings.ReplaceAll(page.text, `\`, `\\`), `)`, `\)`) + ") Tj ET"
		if page.image {
			stream = "q 10 0 0 10 0 0 cm /Im1 Do Q"
		}
		resources := fmt.Sprintf("/Font<</F1 %d 0 R>>", base+1)
		if page.image {
			resources += fmt.Sprintf("/XObject<</Im1 %d 0 R>>", base+3)
		}
		objects = append(objects,
			fmt.Sprintf("<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<<%s>>/Contents %d 0 R>>", resources, base+2),
			"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
			fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", len(stream), stream),
		)
		if page.image {
			objects = append(objects, "<</Type/XObject/Subtype/Image/Width 1/Height 1/ColorSpace/DeviceRGB/BitsPerComponent 8/Length 3>>\nstream\nabc\nendstream")
		} else {
			objects = append(objects, "<</Type/ObjStm/N 0/First 0/Length 0>>\nstream\n\nendstream")
		}
	}

	var out strings.Builder
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return []byte(out.String())
}
