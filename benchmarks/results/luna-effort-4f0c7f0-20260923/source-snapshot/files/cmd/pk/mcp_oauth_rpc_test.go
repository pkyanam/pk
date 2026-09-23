package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/mcpclient"
)

func TestMCPOAuthLoginRPCEventsContainOnlySafeStatus(t *testing.T) {
	var output bytes.Buffer
	s := &rpcServer{ctx: context.Background(), output: &output}
	release := make(chan struct{})
	s.startMCPOAuthLoginWith("req-1", "cloud", func(context.Context, string) error { <-release; return nil })
	s.mu.Lock()
	done := s.skillOperationDone
	s.mu.Unlock()
	close(release)
	<-done
	var events []rpcEvent
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event rpcEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Type != "mcp_auth_status" || events[1].Type != "mcp_auth_status" {
		t.Fatalf("unexpected OAuth events: %#v", events)
	}
	if strings.Contains(output.String(), "https://") || strings.Contains(output.String(), "state") || strings.Contains(output.String(), "code") || strings.Contains(output.String(), "token") {
		t.Fatalf("OAuth event leaked sensitive flow data: %s", output.String())
	}
}

func TestMCPOAuthLogoutClearsOnlyLocalOAuthSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	store := mcpclient.ConfigStore{Home: home}
	if err := store.AddOAuth(mcpclient.ServerConfig{ID: "cloud", URL: "https://example.test/mcp"}); err != nil {
		t.Fatal(err)
	}
	servers, err := store.List()
	if err != nil || len(servers) != 1 {
		t.Fatalf("List() = %v, %v", servers, err)
	}
	if err := store.SetOAuthSession(servers[0].Auth.SecretRef, []byte(`{"opaque":"local token data"}`)); err != nil {
		t.Fatal(err)
	}
	s := &rpcServer{}
	result, err := s.mcpOAuthLogout(context.Background(), "cloud")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "needs_login" || result["local_session_cleared"] != true {
		t.Fatalf("logout result = %#v", result)
	}
	stored, err := store.OAuthSession(servers[0].Auth.SecretRef)
	if err != nil || stored != "" {
		t.Fatalf("OAuthSession() = %q, %v; want cleared", stored, err)
	}
	if _, err := s.mcpOAuthLogout(context.Background(), "missing"); err == nil {
		t.Fatal("logout for missing server unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(home, "mcp-secrets.json")); err != nil {
		t.Fatal(err)
	}
}
