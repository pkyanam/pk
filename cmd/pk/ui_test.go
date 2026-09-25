package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenTUIEnvironmentDisablesRuntimeTelemetry(t *testing.T) {
	environment := setEnvironment([]string{"PATH=/bin", "DO_NOT_TRACK=0", "HOME=/tmp/private"}, map[string]string{"DO_NOT_TRACK": "1", "PK_EXECUTABLE": "/tmp/pk"})
	values := make(map[string]string)
	for _, value := range environment {
		name, entry, ok := strings.Cut(value, "=")
		if ok {
			values[name] = entry
		}
	}
	if values["DO_NOT_TRACK"] != "1" {
		t.Fatalf("frontend DO_NOT_TRACK = %q", values["DO_NOT_TRACK"])
	}
	if values["PK_EXECUTABLE"] != "/tmp/pk" || values["HOME"] != "/tmp/private" {
		t.Fatalf("other environment values were not preserved: %#v", values)
	}
}

func TestResumePickerLaunchUsesFrontendEnvironment(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	uiEntry := filepath.Join(root, "main.js")
	if err := os.WriteFile(uiEntry, []byte("// fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	bun := filepath.Join(bin, "bun")
	script := "#!/bin/sh\nprintf 'picker=%s\\n' \"$PK_RESUME_PICKER\"\nfor arg in \"$@\"; do printf 'arg=%s\\n' \"$arg\"; done\n"
	if err := os.WriteFile(bun, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("PK_UI_ENTRY", uiEntry)
	t.Setenv("PK_RESUME_PICKER", "")
	var stdout, stderr bytes.Buffer
	for _, alias := range []string{"-r", "-resume", "--resume"} {
		stdout.Reset()
		stderr.Reset()
		if code := runMain([]string{alias, "--model", "gpt-6-luna"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("runMain(%s) exit %d: %s", alias, code, stderr.String())
		}
		if got := stdout.String(); !strings.Contains(got, "picker=1") || strings.Contains(got, "arg=--pk-resume-picker") || !strings.Contains(got, "arg=--model") {
			t.Fatalf("%s launch argv/env = %q", alias, got)
		}
	}
}
