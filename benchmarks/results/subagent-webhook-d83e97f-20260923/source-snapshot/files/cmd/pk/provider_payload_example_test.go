package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/modelstream"
	"github.com/pkyanam/pk/internal/runner"
)

// TestProviderPayloadExampleCapture is an opt-in artifact generator. The
// script copies this file into a clean source worktree and sets the gate; the
// normal test suite does not create or rewrite documentation artifacts.
func TestProviderPayloadExampleCapture(t *testing.T) {
	if os.Getenv("PK_CAPTURE_PROVIDER_PAYLOAD") != "1" {
		t.Skip("run scripts/capture-provider-payload to generate the documented local fixture")
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join(workingDir, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	home := filepath.Join(fixture, "home")
	pkHomeDir := filepath.Join(fixture, "pk-home")
	workspace := filepath.Join(fixture, "workspace")
	extDir := filepath.Join(fixture, "extension")
	for _, dir := range []string{home, pkHomeDir, workspace, extDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"README.txt": "Synthetic workspace fixture.\n",
		"notes.txt":  "No user data.\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workerPath := filepath.Join(extDir, "workspace-stats")
	build := exec.Command("go", "build", "-o", workerPath, "./internal/extensions/examples/workspace_stats")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture worker: %v\n%s", err, output)
	}
	t.Setenv("HOME", home)
	t.Setenv("PK_HOME", pkHomeDir)
	manifest := `{"api_version":"pk.extensions/v1","id":"workspace-stats","version":"1.0.0","executable":` + quoteExampleJSON(workerPath) + `,"capabilities":["workspace.read"],"tools":[{"name":"workspace_stats","description":"Count regular files, subdirectories, and bytes in the current workspace.","parameters":{"type":"object","properties":{},"additionalProperties":false}}],"commands":[{"name":"stats","description":"Show file and byte counts for the current workspace."}]}`
	manifestPath := filepath.Join(extDir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var bodies [][]byte
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		index := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if index == 1 {
			_, _ = fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"fixture-tool-call","status":"completed","output":[{"id":"fc-item-1","type":"function_call","call_id":"call_stats","name":"workspace_stats","arguments":"{}","status":"completed"}],"usage":{"input_tokens":123,"output_tokens":12}}}`+"\n\n")
		} else if index == 2 {
			_, _ = fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"fixture-followup","status":"completed","output":[{"id":"msg-1","type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"The isolated fixture contains 2 files and 0 subdirectories.","annotations":[],"logprobs":[]}]}],"usage":{"input_tokens":456,"output_tokens":18}}}`+"\n\n")
		} else {
			_, _ = fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"fixture-turn-two","status":"completed","output":[{"id":"msg-2","type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"This was a synthetic local capture; no external provider was contacted.","annotations":[],"logprobs":[]}]}],"usage":{"input_tokens":789,"output_tokens":14}}}`+"\n\n")
		}
	}))
	defer serverHTTP.Close()
	const fakeToken = "synthetic-token-not-a-credential"
	const fakeAccount = "synthetic-account-not-a-credential"
	model, err := modelstream.NewClient(modelstream.Config{AccessToken: fakeToken, AccountID: fakeAccount, BaseURL: serverHTTP.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: fakeToken, AccountID: fakeAccount}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}
	sessions := filepath.Join(pkHomeDir, "sessions")
	sink := &rpcEventSink{events: make(chan []byte, 4096)}
	var diagnostics bytes.Buffer
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: &diagnostics,
		cfgPath: filepath.Join(pkHomeDir, "config.json"), sessionDir: sessions,
		started: true,
		opts:    runner.Options{Workspace: workspace, SessionDir: sessions, Model: "fixture-model-codex", Effort: "medium", ToolEvents: true},
		adapter: adapter, useCodex: true, pluginPaths: []string{manifestPath}, requestTypes: map[string]string{},
	}
	finishTurn := func(requestID, prompt string) {
		t.Helper()
		finished := make(chan turnDone, 1)
		payload, _ := json.Marshal(map[string]string{"text": prompt})
		server.handle(rpcMessage{Version: 1, ID: requestID, Type: "prompt", Payload: payload}, finished)
		deadline := time.After(20 * time.Second)
		for {
			select {
			case event := <-sink.events:
				var value rpcEvent
				if err := json.Unmarshal(event, &value); err != nil {
					t.Fatal(err)
				}
				if value.Type == "turn_finished" && value.ID == requestID {
					if payload, ok := value.Payload.(map[string]any); ok && payload["text"] == nil {
						t.Fatalf("turn %s lacks final text: %s", requestID, event)
					}
					return
				}
				if value.Type == "error" && value.ID == requestID {
					t.Fatalf("turn %s error: %s", requestID, event)
				}
			case result := <-finished:
				server.completeTurn(result)
			case <-deadline:
				t.Fatalf("turn %s timed out", requestID)
			}
		}
	}
	finishTurn("prompt-1", "Analyze this isolated workspace with workspace_stats and report the counts.")
	if server.session == "" {
		t.Fatal("first run did not create a session")
	}
	finishTurn("prompt-2", "Now restate the counts and identify this as a synthetic local-provider example.")
	mu.Lock()
	captured := append([][]byte(nil), bodies...)
	mu.Unlock()
	if len(captured) != 3 {
		t.Fatalf("captured %d native Responses requests, want 3", len(captured))
	}
	var first map[string]any
	if err := json.Unmarshal(captured[0], &first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(first["tools"]), "workspace_stats") {
		t.Fatalf("plugin tool missing from model request: %s\n%s", fmt.Sprint(first["tools"]), diagnostics.String())
	}
	if !strings.Contains(string(captured[1]), "2 files, 0 directories, 43 bytes") {
		t.Fatalf("captured tool-result follow-up lacks actual fixture output; diagnostics=%s", diagnostics.String())
	}
	if !strings.Contains(string(captured[2]), "Now restate the counts") || !strings.Contains(string(captured[2]), "synthetic local-provider example") || !strings.Contains(string(captured[2]), "The isolated fixture contains 2 files") {
		t.Fatal("third request did not include the next user prompt and prior assistant response")
	}
	for i, body := range captured {
		if strings.Contains(string(body), fakeToken) || strings.Contains(string(body), fakeAccount) {
			t.Fatalf("request %d contains synthetic auth material that should exist only in headers", i+1)
		}
	}
	outputDir := strings.TrimSpace(os.Getenv("PK_CAPTURE_OUTPUT_DIR"))
	if outputDir == "" {
		outputDir = filepath.Join(repo, "docs", "examples", "provider-payload")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, body := range captured {
		var check any
		if err := json.Unmarshal(body, &check); err != nil {
			t.Fatalf("request %d invalid JSON: %v", i+1, err)
		}
		path := filepath.Join(outputDir, fmt.Sprintf("native-codex-turn-%d.json", i+1))
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func quoteExampleJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
