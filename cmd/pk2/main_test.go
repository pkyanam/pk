package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunMainHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"--help"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("--help exit=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"pk2 daemon start", "pk2 run", "beta build"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help missing %q", want)
		}
	}
}

func TestRunMainUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"bogus"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("unknown command exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr = %q, want unknown command", stderr.String())
	}
}

func TestDaemonLifecyclePidfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// status with no pidfile -> stopped
	var out, errOut bytes.Buffer
	if code := runMain([]string{"daemon", "status"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "stopped") {
		t.Errorf("status output = %q, want stopped", out.String())
	}
	// fake pidfile pointing at our own process (alive) -> running
	if err := os.WriteFile(filepath.Join(home, pk2PidFile), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runMain([]string{"daemon", "status"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("status-with-pid exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Errorf("status output = %q, want running", out.String())
	}
	// stop with a pidfile pointing at a dead pid -> not running + stale removed
	if err := os.WriteFile(filepath.Join(home, pk2PidFile), []byte("999999"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runMain([]string{"daemon", "stop"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("stop exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "stale") {
		t.Errorf("stop output = %q, want stale pidfile note", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, pk2PidFile)); !os.IsNotExist(err) {
		t.Errorf("pidfile not removed after stale stop: %v", err)
	}
}
