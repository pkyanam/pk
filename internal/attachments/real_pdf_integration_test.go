package attachments_test

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/attachments"
)

// Opt in with PK_TEST_REAL_PDF=1 to exercise the installed pdftoppm binary.
func TestRealPopplerRendersMixedPDFScannedPage(t *testing.T) {
	if os.Getenv("PK_TEST_REAL_PDF") != "1" {
		t.Skip("set PK_TEST_REAL_PDF=1 to run the real Poppler integration check")
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm is not installed")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	pageDir := filepath.Join(root, "rendered-pages")
	for _, dir := range []string{workspace, pageDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pdfPath := filepath.Join(workspace, "mixed.pdf")
	if err := os.WriteFile(pdfPath, mixedSmokePDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := attachments.Load(context.Background(), workspace, []string{"mixed.pdf"}, attachments.Limits{PDFPageImageDir: pageDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Text, "Cover text") || len(items[0].RenderedPDFPages) != 1 || items[0].RenderedPDFPages[0].Page != 2 {
		t.Fatalf("mixed PDF extraction/render result: %+v", items)
	}
	page := items[0].RenderedPDFPages[0]
	file, err := os.Open(page.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(file); err != nil {
		_ = file.Close()
		t.Fatalf("Poppler page output is not a decodable PNG: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(page.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("rendered page permissions info=%v err=%v", info, err)
	}
}

func mixedSmokePDF() []byte {
	objects := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R 6 0 R]/Count 2>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<</Font<</F1 4 0 R>>>>/Contents 5 0 R>>",
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
		"<</Length 47>>\nstream\nBT /F1 12 Tf 72 720 Td (Cover text) Tj ET\nendstream",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Resources<</XObject<</Im1 8 0 R>>>>/Contents 7 0 R>>",
		"<</Length 25>>\nstream\nq 200 0 0 200 80 300 cm /Im1 Do Q\nendstream",
		"<</Type/XObject/Subtype/Image/Width 1/Height 1/ColorSpace/DeviceRGB/BitsPerComponent 8/Length 3>>\nstream\n\xff\x00\x00\nendstream",
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
