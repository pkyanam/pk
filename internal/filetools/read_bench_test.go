package filetools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readBenchmarkFile(b *testing.B) (string, string) {
	b.Helper()
	workspace := b.TempDir()
	path := filepath.Join(workspace, "fixture.txt")
	var contents strings.Builder
	for line := 1; line <= 10000; line++ {
		fmt.Fprintf(&contents, "line %05d: %s\n", line, strings.Repeat("x", 48))
	}
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	return workspace, path
}

func BenchmarkReadTextPage(b *testing.B) {
	workspace, path := readBenchmarkFile(b)
	b.ReportAllocs()
	b.SetBytes(200 * 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := readText(context.Background(), workspace, path, 5000, 200); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSedSamePage(b *testing.B) {
	_, path := readBenchmarkFile(b)
	b.ReportAllocs()
	b.SetBytes(200 * 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cmd := exec.Command("sed", "-n", "5000,5199p", path)
		if _, err := cmd.Output(); err != nil {
			b.Fatal(err)
		}
	}
}
