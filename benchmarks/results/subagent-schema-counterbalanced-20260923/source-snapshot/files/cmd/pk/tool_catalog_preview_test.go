package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/runner"
)

func TestModelToolCatalogBeforeFirstPromptReturnsSafePreview(t *testing.T) {
	pkHome := t.TempDir()
	t.Setenv("PK_HOME", pkHome)
	t.Setenv("TINYFISH_API_KEY", "fixture-key")
	workspace, sessions := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "mcp-started")
	command := filepath.Join(t.TempDir(), "mcp-fixture.sh")
	script := "#!/bin/sh\nprintf started > " + marker + "\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (mcpclient.ConfigStore{Home: pkHome}).Add(mcpclient.ServerConfig{ID: "configured", Command: command}); err != nil {
		t.Fatal(err)
	}

	server := &rpcServer{
		sessionDir:  sessions,
		pluginPaths: []string{"/explicit/plugin.json"},
		opts:        runner.Options{Workspace: workspace, SkillsDirs: defaultSkillDirs()},
	}
	payload, err := server.modelToolCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Initialized || !payload.Preview || payload.Saved {
		t.Fatalf("pre-prompt catalog state = initialized:%v preview:%v saved:%v", payload.Initialized, payload.Preview, payload.Saved)
	}
	if len(payload.Tools) == 0 {
		t.Fatal("pre-prompt catalog is empty")
	}
	want := []string{"Bash", "ViewImage", "SkillUse", "AskUser", "SubagentStart", "SubagentWait", "WebSearch", "WebFetch"}
	byName := make(map[string]runner.SavedToolSummary, len(payload.Tools))
	for _, item := range payload.Tools {
		byName[item.Name] = item
	}
	for _, name := range want {
		if _, ok := byName[name]; !ok {
			t.Errorf("preview omitted available core tool %q; got %#v", name, payload.Tools)
		}
	}
	if got := strings.Join(payload.Deferred, ","); got != "configured MCP servers,enabled plugins" {
		t.Fatalf("deferred integrations = %q", got)
	}
	if !strings.Contains(payload.Notice, "do not start their processes") && !strings.Contains(payload.Notice, "browsing does not start their processes") {
		t.Fatalf("preview notice does not explain deferred startup: %q", payload.Notice)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("listing tools started configured MCP server: stat marker err=%v", err)
	}
}

func TestModelToolCatalogUsesSavedSnapshotAfterFirstPrompt(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	adapter := &mockModelAdapter{replies: []adapterReply{{response: finalText("ok")}}}
	result, err := runner.Run(context.Background(), runner.Options{
		Prompt: "hello", Workspace: workspace, SessionDir: sessions,
		Model: "gpt-6-luna", Effort: "low", Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &rpcServer{sessionDir: sessions, session: result.SessionID, opts: runner.Options{Workspace: workspace}}
	payload, err := server.modelToolCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !payload.Initialized || payload.Preview || !payload.Saved || len(payload.Tools) == 0 {
		t.Fatalf("saved tool catalog = %#v", payload)
	}
	for _, item := range payload.Tools {
		if item.Name == "Bash" {
			return
		}
	}
	t.Fatalf("saved catalog omitted Bash: %#v", payload.Tools)
}
