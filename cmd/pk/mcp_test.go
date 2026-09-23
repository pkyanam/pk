package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/mcpclient"
)

func TestMCPCLIConfigCommandsDoNotExposeValuesOrStartServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	add := []string{"add", "--id", "demo", "--command", executable, "--arg", "--serve", "--env", "API_TOKEN=private-value"}
	if code := runMCPCommand(context.Background(), add, &stdout, &stderr); code != 0 {
		t.Fatalf("add exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "start only when a run") {
		t.Fatalf("add did not explain explicit startup: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMCPCommand(context.Background(), []string{"list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("list exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "demo") || !strings.Contains(stdout.String(), "API_TOKEN") || strings.Contains(stdout.String(), "private-value") || strings.Contains(stdout.String(), "--serve") {
		t.Fatalf("list output leaked or omitted config metadata: %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMCPCommand(context.Background(), []string{"remove", "demo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "mcp.json")); err != nil {
		t.Fatal(err)
	}
}

func TestMCPCommandsAreReachableThroughMainCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"mcp", "add", "--id", "cli", "--command", executable}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("pk mcp add exit=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runMain([]string{"mcp", "list"}, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "cli") {
		t.Fatalf("pk mcp list exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestMCPCLIConfiguresRemoteHTTPWithoutPrintingCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	var out, errOut bytes.Buffer
	args := []string{"mcp", "add", "--id", "docs", "--url", "https://docs.mcp.cloudflare.com/mcp"}
	if code := runMain(args, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("remote add exit=%d stderr=%q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runMain([]string{"mcp", "list"}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "https://docs.mcp.cloudflare.com/mcp") {
		t.Fatalf("remote list exit=%d output=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestMCPCLIConfiguresAndClearsOAuthWithoutLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	var out, errOut bytes.Buffer
	if code := runMain([]string{"mcp", "add", "--id", "account", "--url", "https://mcp.example.test/mcp", "--auth", "oauth"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("add exit=%d err=%q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runMain([]string{"mcp", "status", "account"}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "needs_login") {
		t.Fatalf("status exit=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runMain([]string{"mcp", "logout", "account"}, strings.NewReader(""), &out, &errOut); code != 0 || !strings.Contains(out.String(), "Cleared OAuth") {
		t.Fatalf("logout exit=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestMCPRPCMutationsAreSanitizedAndNextSessionOnly(t *testing.T) {
	root := t.TempDir()
	pkHome := filepath.Join(root, "pk")
	t.Setenv("PK_HOME", pkHome)
	server := &rpcServer{sessionDir: filepath.Join(pkHome, "sessions")}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := server.mcpAdd(context.Background(), mcpServerConfigFixture(executable))
	if err != nil {
		t.Fatal(err)
	}
	if !payload.NextSessionOnly || len(payload.Servers) != 1 || payload.Servers[0].ID != "demo" || len(payload.Tools) != 0 {
		t.Fatalf("MCP update payload = %#v", payload)
	}
	if strings.Contains(fmt.Sprint(payload), "private-value") {
		t.Fatalf("MCP update payload exposed a secret: %#v", payload)
	}
	payload, err = server.mcpRemove(context.Background(), "demo")
	if err != nil || len(payload.Servers) != 0 || !payload.NextSessionOnly {
		t.Fatalf("MCP remove payload = %#v, err=%v", payload, err)
	}
}

func mcpServerConfigFixture(executable string) mcpclient.ServerConfig {
	return mcpclient.ServerConfig{ID: "demo", Command: executable, Env: map[string]string{"API_TOKEN": "private-value"}}
}
