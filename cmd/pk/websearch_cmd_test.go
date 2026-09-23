package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWebCommandConfigureStatusAndClearNeverEchoSecret(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	t.Setenv("TINYFISH_API_KEY", "")
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
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"web", "status"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not configured") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
