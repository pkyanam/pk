package filetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTextPagesNumberedUTF8LinesAndAbsolutePaths(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(outside, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nβeta\nlast"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readText(context.Background(), workspace, path, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "2 | βeta\n") || !strings.Contains(got, "3 | last\n") || !strings.Contains(got, "[End of file.]\n") {
		t.Fatalf("unexpected read page: %q", got)
	}
	relative, err := readText(context.Background(), outside, "sample.txt", 1, 1)
	if err != nil || !strings.Contains(relative, "1 | alpha\n") || !strings.Contains(relative, "Continue with offset 2") {
		t.Fatalf("relative read = %q, err=%v", relative, err)
	}
}

func TestReadTextOutputCapReturnsExplicitContinuation(t *testing.T) {
	workspace := t.TempDir()
	var source strings.Builder
	for i := 1; i <= 200; i++ {
		source.WriteString(strings.Repeat("x", 100))
		source.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(workspace, "many.txt"), []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readText(context.Background(), workspace, "many.txt", 1, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxReadOutput {
		t.Fatalf("output is %d bytes, over cap %d", len(got), maxReadOutput)
	}
	if !strings.Contains(got, "Output cap reached. Continue with offset ") {
		t.Fatalf("missing continuation: %q", got[len(got)-100:])
	}
	footer := strings.TrimSuffix(strings.Split(got, "[Output cap reached. Continue with offset ")[1], "]\n")
	var next int
	if _, err := fmt.Sscanf(footer, "%d", &next); err != nil {
		t.Fatal(err)
	}
	continued, err := readText(context.Background(), workspace, "many.txt", next, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(continued, fmt.Sprintf("%d | ", next)) {
		t.Fatalf("continuation did not resume at line %d: %q", next, continued)
	}
}

func TestReadTextRejectsLongLinesBinaryAndSpecialFiles(t *testing.T) {
	workspace := t.TempDir()
	long := strings.Repeat("x", maxReadOutput+10)
	if err := os.WriteFile(filepath.Join(workspace, "long.txt"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readText(context.Background(), workspace, "long.txt", 1, 1); err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "byte-range") {
		t.Fatalf("long line error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "binary.bin"), []byte("first\n\x00\xff"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readText(context.Background(), workspace, "binary.bin", 2, 1); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary file error = %v", err)
	}
	if _, err := readText(context.Background(), workspace, ".", 1, 1); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("directory error = %v", err)
	}
	for name, data := range map[string][]byte{"escape.txt": []byte("safe\x1b[31mred\n"), "carriage.txt": []byte("a\rb\n")} {
		if err := os.WriteFile(filepath.Join(workspace, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readText(context.Background(), workspace, name, 1, 1); err == nil || !(strings.Contains(err.Error(), "control") || strings.Contains(err.Error(), "escape")) {
			t.Fatalf("unsafe controls in %s were accepted: %v", name, err)
		}
	}
}

func TestReadTextRejectsInvalidUTF8AndCanceledContext(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "invalid.txt"), []byte{0xff, '\n', 'b', '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readText(context.Background(), workspace, "invalid.txt", 2, 1); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readText(ctx, workspace, "invalid.txt", 1, 1); err == nil {
		t.Fatal("canceled read succeeded")
	}
}

func TestReadTextRejectsInvalidBounds(t *testing.T) {
	for _, bounds := range [][2]int{{0, 1}, {1, 0}, {1, maxReadLimit + 1}} {
		if _, err := readText(context.Background(), t.TempDir(), "x", bounds[0], bounds[1]); err == nil {
			t.Fatalf("accepted bounds %v", bounds)
		}
	}
}
