package attachments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	for _, want := range []string{"text only", "no OCR or page images", "truncated", "first page"} {
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
	if got[0].ScannedPages != 1 || got[0].Text != "" || !strings.Contains(FormatPromptNote(got), "no OCR was run") {
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
