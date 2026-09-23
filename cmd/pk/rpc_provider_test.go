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

func TestRPCProviderPresetAddStoresKeyWithoutEchoAndPreservesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	store := providers.Store{Home: home}
	if err := store.Put(providers.Provider{ID: "groq", Protocol: providers.ProtocolChatCompletions, BaseURL: "https://api.groq.com/openai/v1", APIKey: "old-secret", DefaultModel: "old-model", DefaultEffort: "low"}); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "presets", Type: "provider_presets_list"}, make(chan turnDone, 1))
	presets := readRPCEvent(t, sink)
	if presets.Type != "provider_presets" {
		t.Fatalf("catalog response = %+v", presets)
	}
	server.handle(rpcMessage{Version: 1, ID: "add-preset", Type: "provider_preset_add", Payload: json.RawMessage(`{"preset_id":"groq","api_key":"new-secret"}`)}, make(chan turnDone, 1))
	updated := readRPCEvent(t, sink)
	if updated.Type != "providers_updated" || updated.Payload.(map[string]any)["added_provider_id"] != "groq" {
		t.Fatalf("preset add response = %+v", updated)
	}
	encoded, _ := json.Marshal(updated.Payload)
	if strings.Contains(string(encoded), "new-secret") || strings.Contains(string(encoded), "old-secret") {
		t.Fatalf("preset add response leaked key: %s", encoded)
	}
	got, err := store.Get("groq")
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "new-secret" || got.DefaultModel != "old-model" || got.DefaultEffort != "low" {
		t.Fatalf("preset reconnect lost credentials/defaults: %+v", got)
	}
	accountID := "0123456789abcdef0123456789abcdef"
	server.handle(rpcMessage{Version: 1, ID: "add-cloudflare", Type: "provider_preset_add", Payload: json.RawMessage(`{"preset_id":"cloudflare-workers-ai","account_id":"` + accountID + `","api_key":"cloudflare-private-token"}`)}, make(chan turnDone, 1))
	cloudflareUpdated := readRPCEvent(t, sink)
	cloudflareJSON, _ := json.Marshal(cloudflareUpdated.Payload)
	if cloudflareUpdated.Type != "providers_updated" || strings.Contains(string(cloudflareJSON), "cloudflare-private-token") {
		t.Fatalf("Workers AI setup response=%+v json=%s", cloudflareUpdated, cloudflareJSON)
	}
	cloudflare, err := store.Get("cloudflare-workers-ai")
	if err != nil || cloudflare.Protocol != providers.ProtocolCloudflareWorkersAI || cloudflare.APIKey != "cloudflare-private-token" || cloudflare.BaseURL != "https://api.cloudflare.com/client/v4/accounts/"+accountID+"/ai/v1" {
		t.Fatalf("Workers AI config=%+v err=%v", cloudflare, err)
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
	server.handle(rpcMessage{Version: 1, ID: "select", Type: "provider_select", Payload: json.RawMessage(`{"provider_id":"fixture","model":"discovered-model"}`)}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	if event.Type != "provider_selected" || event.Payload.(map[string]any)["provider_id"] != "fixture" || event.Payload.(map[string]any)["model"] != "discovered-model" || server.opts.Model != "discovered-model" {
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
	server.handle(rpcMessage{Version: 1, ID: "secret-case", Type: "provider_add", Payload: json.RawMessage(`{"id":"secret2","protocol":"chat_completions","base_url":"https://api.example.test/v1","API_KEY":"case-secret"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" {
		t.Fatalf("case-variant literal credential event=%+v", event)
	}
	if _, err := (providers.Store{Home: home}).Get("secret2"); err == nil {
		t.Fatal("RPC persisted case-variant literal API key")
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
	server.handle(rpcMessage{Version: 1, ID: "update-same", Type: "provider_add", Payload: json.RawMessage(`{"id":"rpc","protocol":"chat_completions","base_url":"https://api.example.test/v1","default_model":"model-y"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "providers_updated" {
		t.Fatalf("same-endpoint generic update response=%+v", event)
	}
	stored, err := (providers.Store{Home: home}).Get("rpc")
	if err != nil || stored.APIKeyEnv != "RPC_MODEL_KEY" || stored.DefaultModel != "model-y" {
		t.Fatalf("same-endpoint RPC update lost credential source: %+v err=%v", stored, err)
	}
	server.handle(rpcMessage{Version: 1, ID: "update-changed", Type: "provider_add", Payload: json.RawMessage(`{"id":"rpc","protocol":"chat_completions","base_url":"https://changed.example.test/v1"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "error" || !strings.Contains(event.Payload.(map[string]any)["message"].(string), "configure a new API key source") {
		t.Fatalf("changed-endpoint RPC update response=%+v", event)
	}
	stored, err = (providers.Store{Home: home}).Get("rpc")
	if err != nil || stored.BaseURL != "https://api.example.test/v1" || stored.APIKeyEnv != "RPC_MODEL_KEY" {
		t.Fatalf("failed endpoint change altered existing provider: %+v err=%v", stored, err)
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

func TestRPCProviderModelDiscoveryIsResponsiveAndCoalescesCancellation(t *testing.T) {
	firstEntered := make(chan struct{})
	var calls atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"newest-model"}]}`)
	}))
	defer httpServer.Close()
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	if err := (providers.Store{Home: home}).Put(providers.Provider{ID: "fixture", Protocol: providers.ProtocolChatCompletions, BaseURL: httpServer.URL + "/v1", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 16)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "old", Type: "provider_models", Payload: json.RawMessage(`{"provider_id":"fixture"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.ID != "old" || event.Type != "provider_models_started" {
		t.Fatalf("first discovery start=%+v", event)
	}
	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first discovery did not reach the fixture server")
	}
	// The RPC reader must remain available while the model endpoint is stalled.
	server.handle(rpcMessage{Version: 1, ID: "unrelated", Type: "providers_list"}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.ID != "unrelated" || event.Type != "providers" {
		t.Fatalf("unrelated RPC blocked by discovery: %+v", event)
	}
	server.handle(rpcMessage{Version: 1, ID: "new", Type: "provider_models", Payload: json.RawMessage(`{"provider_id":"fixture"}`)}, make(chan turnDone, 1))
	seenNewStart, seenOldCancel := false, false
	deadline := time.After(3 * time.Second)
	for !seenNewStart || !seenOldCancel {
		select {
		case <-deadline:
			t.Fatal("replacement discovery did not cancel/coalesce old request")
		case raw := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.ID == "new" && event.Type == "provider_models_started" {
				seenNewStart = true
			}
			if event.ID == "old" && event.Type == "provider_models_cancelled" {
				seenOldCancel = true
			}
		}
	}
	for {
		select {
		case <-deadline:
			t.Fatal("replacement discovery did not finish")
		case raw := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.ID == "old" && event.Type == "provider_models" {
				t.Fatalf("stale discovery overwrote replacement: %+v", event)
			}
			if event.ID == "new" && event.Type == "provider_models" {
				if models := event.Payload.(map[string]any)["models"].([]any); len(models) != 1 || models[0].(map[string]any)["id"] != "newest-model" {
					t.Fatalf("replacement model response=%+v", event)
				}
				if calls.Load() != 2 {
					t.Fatalf("model discovery HTTP calls=%d, want two", calls.Load())
				}
				return
			}
		}
	}
}
