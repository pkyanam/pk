package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
)

func TestRPCProviderCatalogSelectionAndRuntime(t *testing.T) {
	home, workspace, sessions := t.TempDir(), t.TempDir(), t.TempDir()
	requests := atomic.Int32{}
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"rpc-model"}]}`)
		case "/v1/chat/completions":
			requests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"rpc-provider-response\",\"choices\":[{\"delta\":{\"content\":\"provider reply\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"rpc-provider-response\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer serverHTTP.Close()
	t.Setenv("PK_HOME", home)
	provider := providers.Provider{ID: "fixture", Protocol: providers.ProtocolChatCompletions, BaseURL: serverHTTP.URL + "/v1", APIKey: "fixture-secret", DefaultModel: "rpc-model", DefaultEffort: "low"}
	store := providers.Store{Home: home}
	if err := store.Put(provider); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: home + "/config.json", sessionDir: sessions, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "providers", Type: "providers_list"}, make(chan turnDone, 1))
	listEvent := readRPCEvent(t, sink)
	if listEvent.Type != "providers" {
		t.Fatalf("providers list event=%+v", listEvent)
	}
	listJSON, _ := json.Marshal(listEvent.Payload)
	if strings.Contains(string(listJSON), "fixture-secret") {
		t.Fatalf("provider list exposed credential: %s", listJSON)
	}
	server.handle(rpcMessage{Version: 1, ID: "models", Type: "provider_models", Payload: json.RawMessage(`{"provider_id":"fixture"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "provider_models_started" {
		t.Fatalf("provider model discovery start=%+v", event)
	}
	modelsEvent := readRPCEvent(t, sink)
	if modelsEvent.Type != "provider_models" {
		t.Fatalf("provider models event=%+v", modelsEvent)
	}
	server.handle(rpcMessage{Version: 1, ID: "start", Type: "start", Payload: json.RawMessage(`{"workspace":"` + workspace + `","provider_id":"fixture","steering":true}`)}, make(chan turnDone, 1))
	ready := readRPCEvent(t, sink)
	if ready.Type != "ready" || ready.Payload.(map[string]any)["provider_id"] != "fixture" {
		t.Fatalf("ready event=%+v", ready)
	}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prompt", Type: "prompt", Payload: json.RawMessage(`{"text":"hello"}`)}, finished)
	select {
	case result := <-finished:
		if result.err != nil || !strings.Contains(result.text, "provider reply") {
			t.Fatalf("provider run result=%+v", result)
		}
		server.completeTurn(result)
	case <-time.After(5 * time.Second):
		t.Fatal("RPC provider run timed out")
	}
	if requests.Load() != 1 {
		t.Fatalf("provider completion requests=%d, want one", requests.Load())
	}
	digest := sha256.Sum256([]byte(server.session))
	snapshotData, err := os.ReadFile(filepath.Join(sessions, hex.EncodeToString(digest[:])+".context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot runner.ContextSnapshot
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		t.Fatal(err)
	}
	if server.providerID != "fixture" || snapshot.ProviderFingerprint != provider.Fingerprint() || snapshot.ProviderID != "fixture" {
		t.Fatalf("RPC provider state id=%q snapshot provider=%q fingerprint=%q", server.providerID, snapshot.ProviderID, snapshot.ProviderFingerprint)
	}
}

func TestRPCProviderSelectOnlyBeforeSessionPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	store := providers.Store{Home: home}
	if err := store.Put(providers.Provider{ID: "fixture", Protocol: providers.ProtocolChatCompletions, BaseURL: "https://api.example.test/v1", APIKey: "secret", DefaultModel: "fixture-model", DefaultEffort: "low"}); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, opts: runner.Options{Model: "gpt-6-luna", Effort: "medium"}, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "select", Type: "provider_select", Payload: json.RawMessage(`{"provider_id":"fixture"}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "provider_selected" || event.Payload.(map[string]any)["provider_id"] != "fixture" || event.Payload.(map[string]any)["model"] != "fixture-model" {
		t.Fatalf("provider selection event=%+v", event)
	}
	server.session = "saved-session"
	server.handle(rpcMessage{Version: 1, ID: "select-again", Type: "provider_select", Payload: json.RawMessage(`{"provider_id":""}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" {
		t.Fatalf("post-prompt provider selection event=%+v", event)
	}
}

func TestRPCProviderMutationRoutesRedactSecretsAndRespectActiveSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "secret", Type: "provider_add", Payload: json.RawMessage(`{"id":"secret","protocol":"chat_completions","base_url":"https://api.example.test/v1","api_key":"literal-secret"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" {
		t.Fatalf("literal credential event=%+v", event)
	}
	if _, err := (providers.Store{Home: home}).Get("secret"); err == nil {
		t.Fatal("RPC persisted a literal API key")
	}
	server.handle(rpcMessage{Version: 1, ID: "add", Type: "provider_add", Payload: json.RawMessage(`{"id":"rpc","protocol":"chat_completions","base_url":"https://api.example.test/v1","api_key_env":"RPC_MODEL_KEY","default_model":"model-x"}`)}, make(chan turnDone, 1))
	added := readRPCEvent(t, sink)
	if added.Type != "providers_updated" {
		t.Fatalf("provider add response=%+v", added)
	}
	addedJSON, _ := json.Marshal(added.Payload)
	if strings.Contains(string(addedJSON), "literal-secret") || strings.Contains(string(addedJSON), "RPC_MODEL_KEY") == false {
		t.Fatalf("provider add summary leaked credential or omitted safe env name: %s", addedJSON)
	}
	server.handle(rpcMessage{Version: 1, ID: "default", Type: "provider_default", Payload: json.RawMessage(`{"provider_id":"rpc"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "providers_updated" || event.Payload.(map[string]any)["default_provider_id"] != "rpc" {
		t.Fatalf("provider default response=%+v", event)
	}
	server.providerID, server.session = "rpc", "existing"
	server.handle(rpcMessage{Version: 1, ID: "remove", Type: "provider_remove", Payload: json.RawMessage(`{"id":"rpc"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" || !strings.Contains(event.Payload.(map[string]any)["message"].(string), "pinned") {
		t.Fatalf("selected provider remove response=%+v", event)
	}
	if _, err := (providers.Store{Home: home}).Get("rpc"); err != nil {
		t.Fatalf("selected provider was removed: %v", err)
	}
}
