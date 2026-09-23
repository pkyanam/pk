package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGenerateCopiesOnlyInvocationArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	root := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex")
	thread := "01a0cc58-0b27-7c20-8f9b-3efe81d0553b"
	fixture := filepath.Join(t.TempDir(), "codex-fixture")
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	pngBytes := pngData.Bytes()
	decoy := filepath.Join(codexHome, "generated_images", "01a0cc57-5cd7-78d2-b033-aff9b285c8d7")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	var decoyPNG bytes.Buffer
	if err := png.Encode(&decoyPNG, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "decoy.png"), decoyPNG.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(pngBytes)
	script := "#!/bin/sh\n" +
		"mkdir -p \"$CODEX_HOME/generated_images/" + thread + "\"\n" +
		"printf '%s' '" + encoded + "' | base64 -d > \"$CODEX_HOME/generated_images/" + thread + "/exec-one.png\"\n" +
		"printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\n"
	if err := os.WriteFile(fixture, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Generate(context.Background(), Config{Executable: fixture, CodexHome: codexHome, DriverModel: "gpt-6-astra", Effort: "low", Timeout: time.Second}, Request{Prompt: "mint leaf", OutputRoot: root, OutputPath: "images/leaf.png"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MIME != "image/png" || got.Width != 1 || got.Height != 1 || got.Bytes != int64(len(pngBytes)) {
		t.Fatalf("unexpected result: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "images/leaf.png")); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRequiresOneArtifactFromExactThread(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	thread := "01a0cc58-0b27-7c20-8f9b-3efe81d0553b"
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing exact thread", body: "mkdir -p \"$CODEX_HOME/generated_images/other-thread\"\nprintf '%s' 'not png' > \"$CODEX_HOME/generated_images/other-thread/decoy.png\"\n", want: "thread directory is missing"},
		{name: "multiple images", body: "mkdir -p \"$CODEX_HOME/generated_images/" + thread + "\"\nprintf '%s' 'not png' > \"$CODEX_HOME/generated_images/" + thread + "/one.png\"\nprintf '%s' 'not png' > \"$CODEX_HOME/generated_images/" + thread + "/two.png\"\n", want: "multiple images"},
		{name: "invalid PNG", body: "mkdir -p \"$CODEX_HOME/generated_images/" + thread + "\"\nprintf '%s' 'truncated' > \"$CODEX_HOME/generated_images/" + thread + "/bad.png\"\n", want: "not a valid PNG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codexHome := filepath.Join(t.TempDir(), "codex")
			fixture := filepath.Join(t.TempDir(), "codex-fixture")
			script := "#!/bin/sh\n" + tc.body + "printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\n"
			if err := os.WriteFile(fixture, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := Generate(context.Background(), Config{Executable: fixture, CodexHome: codexHome, DriverModel: "gpt-6-astra", Timeout: time.Second}, Request{Prompt: "mint leaf", OutputRoot: t.TempDir(), OutputPath: "leaf.png"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestGenerateRejectsMalformedOrOversizedJSONL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"malformed", "printf '{not-json}\\n'\n", "invalid JSONL event"},
		{"oversized", "head -c 1048577 /dev/zero | tr '\\000' x\nprintf '\\n'\n", "exceeded size limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "codex-fixture")
			if err := os.WriteFile(fixture, []byte("#!/bin/sh\n"+tc.body), 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := Generate(context.Background(), Config{Executable: fixture, CodexHome: t.TempDir(), DriverModel: "gpt-6-astra", Timeout: time.Second}, Request{Prompt: "x", OutputRoot: t.TempDir(), OutputPath: "x.png"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestGenerateRejectsSymlinkArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	thread := "01a0cc58-0b27-7c20-8f9b-3efe81d0553b"
	codexHome := filepath.Join(t.TempDir(), "codex")
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("not png"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "codex-fixture")
	script := "#!/bin/sh\nmkdir -p \"$CODEX_HOME/generated_images/" + thread + "\"\nln -s '" + outside + "' \"$CODEX_HOME/generated_images/" + thread + "/link.png\"\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\n"
	if err := os.WriteFile(fixture, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Generate(context.Background(), Config{Executable: fixture, CodexHome: codexHome, DriverModel: "gpt-6-astra", Timeout: time.Second}, Request{Prompt: "x", OutputRoot: t.TempDir(), OutputPath: "x.png"})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestGenerateRequiresExplicitDriverAndNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	if _, err := Generate(context.Background(), Config{}, Request{Prompt: "x", OutputRoot: root, OutputPath: "x.png"}); err == nil || !strings.Contains(err.Error(), "driver model") {
		t.Fatalf("expected explicit driver requirement, got %v", err)
	}
	if _, err := safeNewOutput(root, "../escape.png"); err == nil {
		t.Fatal("accepted escaping path")
	}
	if err := os.WriteFile(filepath.Join(root, "exists.png"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := safeNewOutput(root, "exists.png"); err == nil {
		t.Fatal("accepted overwrite")
	}
}

func TestInvalidDestinationFailsBeforeWorkerStarts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	fixture := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "exists.png"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Generate(context.Background(), Config{Executable: fixture, DriverModel: "gpt-6-astra"}, Request{Prompt: "x", OutputRoot: root, OutputPath: "exists.png"})
	if err == nil {
		t.Fatal("expected existing destination to fail")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("worker started before destination validation")
	}
}

func TestWorkerEnvironmentDoesNotForwardAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-only-value")
	env := workerEnvironment(t.TempDir())
	for _, item := range env {
		if strings.HasPrefix(item, "OPENAI_API_KEY=") {
			t.Fatal("worker inherited API key environment")
		}
	}
}

func TestGenerateRejectsSymlinkedOutputParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip(err)
	}
	if _, err := safeNewOutput(root, "linked/out.png"); err == nil {
		t.Fatal("accepted symlinked parent")
	}
}

func TestGenerateTimeoutKillsWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a shell executable")
	}
	fixture := filepath.Join(t.TempDir(), "slow-codex")
	thread := "01a0cc58-0b27-7c20-8f9b-3efe81d0553b"
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\nsleep 30\n"
	if err := os.WriteFile(fixture, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := Generate(context.Background(), Config{Executable: fixture, CodexHome: t.TempDir(), DriverModel: "gpt-6-astra", Timeout: 100 * time.Millisecond}, Request{Prompt: "x", OutputRoot: t.TempDir(), OutputPath: "x.png"})
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("worker cancellation took too long: %s", time.Since(start))
	}
}
