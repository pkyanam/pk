package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
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
	if event.Type != "provider_selected" || event.Payload.(map[string]any)["provider_id"] != "fixture" || event.Payload.(map[string]any)["model"] != "discovered-model" || event.Payload.(map[string]any)["persisted"] != true || server.opts.Model != "discovered-model" {
		t.Fatalf("provider selection event=%+v", event)
	}
	stored, err := store.Get("fixture")
	if err != nil || stored.DefaultModel != "fixture-model" {
		t.Fatalf("connection config was unexpectedly overwritten: provider=%+v err=%v", stored, err)
	}
	preference, found, err := store.ModelPreference("fixture")
	if err != nil || !found || preference.Model != "discovered-model" {
		t.Fatalf("selection preference did not persist: %+v found=%v err=%v", preference, found, err)
	}
	if defaultID, err := store.DefaultID(); err != nil || defaultID != "fixture" {
		t.Fatalf("selected provider default did not persist: %q err=%v", defaultID, err)
	}
	workspace := t.TempDir()
	secondSink := &rpcEventSink{events: make(chan []byte, 8)}
	second := &rpcServer{ctx: context.Background(), output: secondSink, diagnostics: io.Discard, cfgPath: filepath.Join(home, "config.json"), sessionDir: filepath.Join(home, "sessions"), requestTypes: map[string]string{}}
	second.handle(rpcMessage{Version: 1, ID: "restart", Type: "start", Payload: json.RawMessage(`{"workspace":"` + workspace + `"}`)}, make(chan turnDone, 1))
	ready := readRPCEvent(t, secondSink)
	if ready.Type != "ready" || ready.Payload.(map[string]any)["provider_id"] != "fixture" || ready.Payload.(map[string]any)["model"] != "discovered-model" {
		t.Fatalf("fresh RPC process did not restore selected provider/model: %+v", ready)
	}
	second.session = "saved-session"
	second.handle(rpcMessage{Version: 1, ID: "select-again", Type: "provider_select", Payload: json.RawMessage(`{"provider_id":""}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, secondSink); event.Type != "error" {
		t.Fatalf("post-prompt provider selection event=%+v", event)
	}
}

func TestRPCSetModelPersistsProviderScopedPreference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	cfg := config.Defaults()
	cfg.Model = "native-model"
	cfg.Effort = "medium"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	store := providers.Store{Home: home}
	provider := providers.Provider{ID: "fixture", Protocol: providers.ProtocolChatCompletions, BaseURL: "https://api.example.test/v1", APIKey: "secret", SupportsReasoningEffort: true, DefaultModel: "connection-default", DefaultEffort: "low"}
	if err := store.Put(provider); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 4)}
	server := &rpcServer{ctx: context.Background(), output: sink, started: true, providerID: provider.ID, opts: runner.Options{ProviderID: provider.ID, Model: "session-model", Effort: "low"}, cfgPath: cfgPath, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "set", Type: "set_model", Payload: json.RawMessage(`{"model":"new-model","effort":"high"}`)}, make(chan turnDone, 1))
	if event := readRPCEvent(t, sink); event.Type != "status" {
		t.Fatalf("set_model response = %+v", event)
	}
	preference, found, err := (providers.Store{Home: home}).ModelPreference(provider.ID)
	if err != nil || !found || preference.Model != "new-model" || preference.Effort != "high" {
		t.Fatalf("provider preference = %+v found=%v err=%v", preference, found, err)
	}
	global, err := config.Load(cfgPath)
	if err != nil || global.Model != "native-model" || global.Effort != "medium" {
		t.Fatalf("provider set_model polluted native defaults: model=%q effort=%q err=%v", global.Model, global.Effort, err)
	}
	reloaded, err := (providers.Store{Home: home}).Get(provider.ID)
	if err != nil || reloaded.Fingerprint() != provider.Fingerprint() {
		t.Fatalf("model change altered provider identity: provider=%+v err=%v", reloaded, err)
	}
}

func TestRPCRejectsLegacyCloudflareModelOnNativeRouteButAllowsReselect(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("PK_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	cfg := config.Defaults()
	cfg.Model = "@cf/zai-org/glm-5.3-flash"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	store := providers.Store{Home: home}
	if err := store.Put(providers.Provider{ID: "cloudflare", Protocol: providers.ProtocolCloudflareWorkersAI, BaseURL: "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1", APIKey: "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 8)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: cfgPath, sessionDir: filepath.Join(home, "sessions"), requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "start", Type: "start", Payload: json.RawMessage(`{"workspace":"` + workspace + `","provider_id":"native"}`)}, make(chan turnDone, 1))
	ready := readRPCEvent(t, sink)
	if ready.Type != "ready" || ready.Payload.(map[string]any)["provider_id"] != "" {
		t.Fatalf("startup should remain usable for provider repair: %+v", ready)
	}
	server.handle(rpcMessage{Version: 1, ID: "prompt", Type: "prompt", Payload: json.RawMessage(`{"text":"hello"}`)}, make(chan turnDone, 1))
	promptError := readRPCEvent(t, sink)
	if promptError.Type != "error" || !strings.Contains(promptError.Payload.(map[string]any)["message"].(string), "requires the Cloudflare Workers AI provider") || server.active {
		t.Fatalf("native request was not blocked safely: event=%+v active=%v", promptError, server.active)
	}
	server.handle(rpcMessage{Version: 1, ID: "select-cloudflare", Type: "provider_select", Payload: json.RawMessage(`{"provider_id":"cloudflare","model":"@cf/zai-org/glm-5.3-flash"}`)}, make(chan turnDone, 1))
	selected := readRPCEvent(t, sink)
	if selected.Type != "provider_selected" || selected.Payload.(map[string]any)["persisted"] != true || server.providerID != "cloudflare" {
		t.Fatalf("legacy model recovery selection = %+v provider=%q", selected, server.providerID)
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

func TestRPCProviderChatStreamingProgressFinalHistoryAndCancel(t *testing.T) {
	home, workspace, sessions := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PK_HOME", home)
	firstStarted := make(chan int, 1)
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	secondCanceled := make(chan struct{}, 1)
	var calls atomic.Int32
	writeChunk := func(w http.ResponseWriter, chunk string) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode provider request: %v", err)
			return
		}
		if len(request.Messages) == 0 {
			t.Error("provider request had no messages")
			return
		}
		call := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if call == 1 {
			promptBytes := 0
			for _, message := range request.Messages {
				promptBytes += len(message.Content)
			}
			firstStarted <- promptBytes
			writeChunk(w, "{\"id\":\"chat-stream-1\",\"choices\":[{\"delta\":{\"reasoning_content\":\"private reasoning that must not be rendered\"}}]}")
			writeChunk(w, "{\"id\":\"chat-stream-1\",\"choices\":[{\"delta\":{\"content\":\"A streamed answer \"}}]}")
			select {
			case <-firstRelease:
			case <-r.Context().Done():
				return
			}
			writeChunk(w, "{\"id\":\"chat-stream-1\",\"choices\":[{\"delta\":{\"content\":\"finishes here.\"},\"finish_reason\":\"stop\"}]}")
			writeChunk(w, "[DONE]")
			return
		}
		writeChunk(w, "{\"id\":\"chat-stream-2\",\"choices\":[{\"delta\":{\"content\":\"Cancellation fixture draft.\"}}]}")
		secondStarted <- struct{}{}
		<-r.Context().Done()
		close(secondCanceled)
	}))
	defer httpServer.Close()
	provider := providers.Provider{
		ID: "fixture", Protocol: providers.ProtocolChatCompletions,
		BaseURL: httpServer.URL + "/v1", APIKey: "fixture-secret",
		DefaultModel: "fixture-model", DefaultEffort: "low",
	}
	if err := (providers.Store{Home: home}).Put(provider); err != nil {
		t.Fatal(err)
	}
	sink := &rpcEventSink{events: make(chan []byte, 256)}
	rpc := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard,
		cfgPath: filepath.Join(home, "config.json"), sessionDir: sessions,
		started: true, providerID: provider.ID,
		opts:         runner.Options{Workspace: workspace, SessionDir: sessions, Model: provider.DefaultModel, Effort: provider.DefaultEffort},
		requestTypes: map[string]string{},
	}
	longPrompt := "Please inspect this long request carefully. " + strings.Repeat("Keep the answer grounded in the supplied requirement. ", 350)
	firstPayload, err := json.Marshal(map[string]any{"text": longPrompt})
	if err != nil {
		t.Fatal(err)
	}
	firstFinished := make(chan turnDone, 1)
	rpc.handle(rpcMessage{Version: 1, ID: "chat-first", Type: "prompt", Payload: firstPayload}, firstFinished)
	select {
	case size := <-firstStarted:
		if size < len(longPrompt) {
			t.Fatalf("long prompt bytes in provider request=%d, want at least %d", size, len(longPrompt))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not begin the delayed streaming response")
	}
	gotText, gotReasoning := false, false
	deadline := time.After(5 * time.Second)
	for !gotText || !gotReasoning {
		select {
		case raw := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.ID != "chat-first" || event.Type != "model_progress" {
				continue
			}
			payload, _ := event.Payload.(map[string]any)
			switch payload["phase"] {
			case "assistant_delta":
				gotText = true
				if !strings.Contains(fmt.Sprint(payload["text_delta"]), "A streamed answer") {
					t.Fatalf("assistant progress payload=%v", payload)
				}
			case "reasoning_progress":
				gotReasoning = true
				if payload["text_delta"] != nil || payload["reasoning"] != nil {
					t.Fatalf("reasoning progress exposed provider reasoning: %v", payload)
				}
			}
		case <-deadline:
			t.Fatalf("stream progress did not arrive before completion: assistant=%v reasoning=%v", gotText, gotReasoning)
		}
	}
	select {
	case result := <-firstFinished:
		t.Fatalf("turn finished before provider completion was released: %+v", result)
	default:
	}
	sessionID := rpc.session
	if sessionID == "" {
		t.Fatal("session was not created before stream completion")
	}
	close(firstRelease)
	var first turnDone
	select {
	case first = <-firstFinished:
		if first.err != nil || !strings.Contains(first.text, "A streamed answer finishes here.") {
			t.Fatalf("completed first turn=%+v", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat turn did not finish after provider completion")
	}
	rpc.completeTurn(first)
	store, err := localfile.New(sessions)
	if err != nil {
		t.Fatal(err)
	}
	history, _, err := recentSessionHistory(context.Background(), store, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	historyText := fmt.Sprint(history)
	if !strings.Contains(historyText, "A streamed answer finishes here.") {
		t.Fatalf("final response missing from history: %+v", history)
	}
	if strings.Contains(historyText, "private reasoning") {
		t.Fatalf("private reasoning appeared in saved history: %+v", history)
	}

	secondPayload, err := json.Marshal(map[string]any{"text": "cancel this streamed response"})
	if err != nil {
		t.Fatal(err)
	}
	secondFinished := make(chan turnDone, 1)
	rpc.handle(rpcMessage{Version: 1, ID: "chat-cancel", Type: "prompt", Payload: secondPayload}, secondFinished)
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second provider request did not start")
	}
	rpc.handle(rpcMessage{Version: 1, ID: "cancel-chat", Type: "cancel"}, make(chan turnDone, 1))
	select {
	case <-secondCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not promptly cancel the provider stream")
	}
	select {
	case canceled := <-secondFinished:
		if canceled.err == nil || !strings.Contains(strings.ToLower(canceled.err.Error()), "cancel") {
			t.Fatalf("canceled stream result=%+v", canceled)
		}
		rpc.completeTurn(canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("RPC worker did not settle after stream cancellation")
	}
}
