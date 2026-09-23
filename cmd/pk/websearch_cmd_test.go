package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/websearch"
)

func TestWebCommandConfigureStatusAndClearNeverEchoSecret(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	t.Setenv("TINYFISH_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	secret := "tinyfish-command-secret-do-not-print"
	var stdout, stderr bytes.Buffer
	if code := runWebCommand(context.Background(), []string{"configure"}, strings.NewReader(secret+"\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("configure exit=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatal("configure echoed credential")
	}
	stdout.Reset()
	stderr.Reset()
	if code := runWebCommand(context.Background(), []string{"status"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("status exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "configured via local_store") || strings.Contains(stdout.String(), secret) {
		t.Fatalf("status output=%q", stdout.String())
	}
	stdout.Reset()
	if code := runWebCommand(context.Background(), []string{"clear"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("clear exit=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	if code := runWebCommand(context.Background(), []string{"status"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("status after clear exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not configured") {
		t.Fatalf("status after clear output=%q", stdout.String())
	}
}

func TestWebCommandUsageRejectsUnexpectedArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWebCommand(context.Background(), []string{"status", "extra"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage: pk web") {
		t.Fatalf("usage output=%q", stderr.String())
	}
}

func TestRunMainDispatchesWebStatus(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	t.Setenv("TINYFISH_API_KEY", "")
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"web", "status"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not configured") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestWebSearchSelectionPrefersDirectKeyThenMonid(t *testing.T) {
	monidDir := t.TempDir()
	monidPath := filepath.Join(monidDir, "monid")
	// Keep JSON in a printf argument and use a plain %s format. Backslash-
	// escaped quotes inside printf's format are not portable across /bin/sh
	// implementations (dash may preserve them, yielding invalid JSON).
	if err := os.WriteFile(monidPath, []byte("#!/bin/sh\nprintf '%s\\n' '[{\"active\":true}]'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(monidPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", monidDir)
	t.Setenv("PK_HOME", t.TempDir())
	t.Setenv("TINYFISH_API_KEY", "")
	config, source, err := resolveWebSearchConfig(context.Background())
	if err != nil || source != "monid_cli" {
		t.Fatalf("Monid selection = source %q err %v", source, err)
	}
	if _, ok := config.Backend.(*websearch.MonidCLIClient); !ok {
		t.Fatalf("Monid selection backend = %T", config.Backend)
	}
	t.Setenv("TINYFISH_API_KEY", "direct-key")
	config, source, err = resolveWebSearchConfig(context.Background())
	if err != nil || source != "environment" || config.APIKey != "direct-key" || config.Backend != nil {
		t.Fatalf("direct selection = config %#v source %q err %v", config, source, err)
	}
}

func TestWebSearchMonidCredentialProbeHonorsCallerCancellation(t *testing.T) {
	monidDir := t.TempDir()
	monidPath := filepath.Join(monidDir, "monid")
	if err := os.WriteFile(monidPath, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(monidPath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", monidDir)
	t.Setenv("PK_HOME", t.TempDir())
	t.Setenv("TINYFISH_API_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := resolveWebSearchConfig(ctx)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("resolveWebSearchConfig() err=%v elapsed=%s", err, time.Since(start))
	}
}
