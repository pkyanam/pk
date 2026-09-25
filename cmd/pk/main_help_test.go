package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTopLevelHelpAccuratelySeparatesInterfacesAndCommands(t *testing.T) {
	pkHome := filepath.Join(t.TempDir(), "pk-home-must-not-be-created")
	t.Setenv("PK_HOME", pkHome)
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"--help"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("--help exit=%d stderr=%q", code, stderr.String())
	}
	help := stdout.String()
	for _, want := range []string{
		"pk [OPTIONS]                  open the full-screen TUI",
		"pk --plain [OPTIONS]          use the line-based interactive interface",
		"pk -p PROMPT [OPTIONS]        run one prompt and exit (canonical one-shot form)",
		"pk web status|setup|configure|clear  configure TinyFish Search and Fetch",
		"pk update [--source DIR]      install the latest official paired release",
		"pk rpc                        run the versioned JSONL backend over stdin/stdout",
		"TUI commands: /help, /exit, /new, /tasks, /sessions, /goal, /model, /effort,",
		"/provider, /plugins, /mcp, /skills, /history, /update.",
		"Plain-mode commands: /help, /exit, /quit. Ctrl-C cancels the active run and exits.",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q", want)
		}
	}
	for _, stale := range []string{"official GitHub main", "web browser", "__task-worker"} {
		if strings.Contains(help, stale) {
			t.Errorf("help contains stale/internal text %q", stale)
		}
	}
	if _, err := os.Stat(pkHome); !os.IsNotExist(err) {
		t.Fatalf("help created pk state at %q: %v", pkHome, err)
	}
}
