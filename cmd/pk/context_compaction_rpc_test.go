package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/providers"
)

func TestRPCManualCompactionRebuildsSavedToolRegistryAndResumes(t *testing.T) {
	home, workspace, sessions := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PK_HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	var requests atomic.Int32
	var bashToolSeen atomic.Bool
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if tools, ok := request["tools"].([]any); ok {
			for _, raw := range tools {
				if tool, ok := raw.(map[string]any); ok {
					function, _ := tool["function"].(map[string]any)
					if function["name"] == "Bash" {
						bashToolSeen.Store(true)
					}
				}
			}
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"compact-fixture\",\"choices\":[{\"delta\":{\"content\":\"ack\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"compact-fixture\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer serverHTTP.Close()
	provider := providers.Provider{ID: "fixture", Protocol: providers.ProtocolChatCompletions, BaseURL: serverHTTP.URL + "/v1", APIKey: "fixture-secret", DefaultModel: "fixture-model", DefaultEffort: "low"}
	if err := (providers.Store{Home: home}).Put(provider); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 256)}
	s := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), sessionDir: sessions, requestTypes: map[string]string{}}
	s.handle(rpcMessage{Version: 1, ID: "start", Type: "start", Payload: json.RawMessage(fmt.Sprintf(`{"workspace":%q,"provider_id":"fixture"}`, workspace))}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "ready" {
		t.Fatalf("start event=%+v", event)
	}
	for i := 0; i < 3; i++ {
		promptID := fmt.Sprintf("prompt-%d", i)
		payload, _ := json.Marshal(map[string]string{"text": fmt.Sprintf("turn %d: %s", i, strings.Repeat("context ", 3500))})
		finished := make(chan turnDone, 1)
		s.handle(rpcMessage{Version: 1, ID: promptID, Type: "prompt", Payload: payload}, finished)
		select {
		case result := <-finished:
			if result.err != nil {
				t.Fatalf("prompt %d: %v", i, result.err)
			}
			s.completeTurn(result)
		case <-time.After(5 * time.Second):
			t.Fatalf("prompt %d timed out", i)
		}
	}
	if !bashToolSeen.Load() {
		t.Fatal("fake provider never observed saved session tool schemas")
	}
	originalSessionID := s.session
	compactID := "compact"
	s.handle(rpcMessage{Version: 1, ID: compactID, Type: "compact", Payload: json.RawMessage(`{}`)}, make(chan turnDone, 1))
	deadline := time.After(10 * time.Second)
	finishedCompact := false
	for !finishedCompact {
		select {
		case <-deadline:
			t.Fatal("manual compaction timed out")
		default:
		}
		event := readRPCEvent(t, sink)
		if event.ID != compactID {
			continue
		}
		switch event.Type {
		case "compact_failed":
			t.Fatalf("manual compaction failed: %+v", event)
		case "compact_finished":
			finishedCompact = true
		}
	}
	if originalSessionID == "" || s.session != originalSessionID || s.opts.SessionID != originalSessionID {
		t.Fatal("session identity changed during compaction")
	}
	finished := make(chan turnDone, 1)
	s.handle(rpcMessage{Version: 1, ID: "after-compact", Type: "prompt", Payload: json.RawMessage(`{"text":"continue after compact"}`)}, finished)
	select {
	case result := <-finished:
		if result.err != nil || !strings.Contains(result.text, "ack") {
			t.Fatalf("resume after compact: result=%+v", result)
		}
		s.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("resume after compaction timed out")
	}
	if requests.Load() < 5 {
		t.Fatalf("provider request count=%d, want prompts + compaction + resumed prompt", requests.Load())
	}
}
