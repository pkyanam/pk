package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestNormalizeBaseURLAndRejectUnsafeValues(t *testing.T) {
	cases := []struct{ input, want string }{
		{"https://api.example.test", "https://api.example.test/v1"},
		{"https://api.example.test/v1/", "https://api.example.test/v1"},
		{"http://127.0.0.1:8181", "http://127.0.0.1:8181/v1"},
		{"http://[::1]:8181/v1", "http://[::1]:8181/v1"},
	}
	for _, item := range cases {
		got, err := NormalizeBaseURL(item.input)
		if err != nil || got != item.want {
			t.Errorf("NormalizeBaseURL(%q)=(%q,%v), want %q", item.input, got, err, item.want)
		}
	}
	for _, input := range []string{"file:///tmp/api", "https://user:pass@example.test/v1", "https://example.test/v1?token=secret", "https://example.test/responses", "http://api.example.test/v1"} {
		if _, err := NormalizeBaseURL(input); err == nil {
			t.Errorf("NormalizeBaseURL(%q) unexpectedly succeeded", input)
		}
	}
}

func TestStorePersistsPrivateConfigAndRedactsSummaries(t *testing.T) {
	home := filepath.Join(t.TempDir(), "providers")
	store := Store{Home: home}
	secret := "key-do-not-show"
	if err := store.Put(Provider{ID: "local", Protocol: ProtocolResponses, BaseURL: "http://127.0.0.1:9000", APIKey: secret, DefaultModel: "model-x", DefaultEffort: "low"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "providers.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("provider config mode=%v err=%v", info, err)
	}
	summaries, err := store.Summaries()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(summaries)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), `"api_key"`) || len(summaries) != 1 || !summaries[0].APIKeyConfigured {
		t.Fatalf("provider summary leaked secret or lost config: %s", encoded)
	}
	loaded, err := store.Get("local")
	if err != nil || loaded.APIKey != secret {
		t.Fatalf("private provider config did not roundtrip: %+v err=%v", loaded, err)
	}
	if err := store.Remove("local"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDefaultSelectionAndRemoval(t *testing.T) {
	store := Store{Home: filepath.Join(t.TempDir(), "providers")}
	for _, id := range []string{"alpha", "beta"} {
		if err := store.Put(Provider{ID: id, Protocol: ProtocolResponses, BaseURL: "http://127.0.0.1:9000"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetDefault("beta"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.DefaultID(); err != nil || got != "beta" {
		t.Fatalf("default=%q err=%v", got, err)
	}
	summaries, err := store.Summaries()
	if err != nil || len(summaries) != 2 || !summaries[1].IsDefault {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	if err := store.Remove("beta"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.DefaultID(); err != nil || got != "" {
		t.Fatalf("default after removal=%q err=%v", got, err)
	}
	if err := store.SetDefault("missing"); err == nil {
		t.Fatal("selected an unconfigured provider")
	}
}

func TestAPIKeyEnvironmentLookupAndFingerprintDoNotExposeSecret(t *testing.T) {
	provider := Provider{ID: "remote", Protocol: ProtocolChatCompletions, BaseURL: "https://api.example.test/v1", APIKeyEnv: "MODEL_API_KEY"}
	key, err := provider.APIKeyWithLookup(func(name string) (string, bool) { return "super-secret", name == "MODEL_API_KEY" })
	if err != nil || key != "super-secret" {
		t.Fatalf("APIKeyWithLookup=(%q,%v)", key, err)
	}
	fingerprint := provider.Fingerprint()
	if fingerprint == "" || strings.Contains(fingerprint, key) {
		t.Fatalf("invalid or exposed provider fingerprint %q", fingerprint)
	}
	missing, err := provider.APIKeyWithLookup(func(string) (string, bool) { return "", false })
	if err == nil || missing != "" || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("missing key result=(%q,%v)", missing, err)
	}
}

func TestChatCompletionsStreamsTextToolsUsageAndModels(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"z-model"},{"id":"a-model"},{"id":"a-model"}]}`)
		case "/v1/chat/completions":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode chat body: %v", err)
			}
			if payload["stream"] != true || payload["model"] != "fixture-model" || payload["reasoning_effort"] != "medium" {
				t.Errorf("unexpected chat request: %#v", payload)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			for _, event := range []string{
				`data: {"id":"chat-1","choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}` + "\n\n",
				`data: {"id":"chat-1","choices":[{"delta":{"content":"lo"},"finish_reason":null}]}` + "\n\n",
				`data: {"id":"chat-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"weather","arguments":"{\"city\":"}}]},"finish_reason":null}]}` + "\n\n",
				`data: {"id":"chat-1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
				`data: {"id":"chat-1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":6,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}` + "\n\n",
				"data: [DONE]\n\n",
			} {
				_, _ = io.WriteString(w, event)
				flusher.Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := Provider{ID: "fixture", Protocol: ProtocolChatCompletions, BaseURL: server.URL + "/v1", APIKey: "fixture-secret", DefaultModel: "fixture-model", SupportsReasoningEffort: true}
	models, err := provider.Models(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "a-model" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if authorization != "Bearer fixture-secret" {
		t.Fatalf("models request auth=%q", authorization)
	}
	client, err := NewClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var streamed strings.Builder
	ctx := WithObserver(context.Background(), func(event Event) {
		if event.Kind == "assistant_delta" {
			streamed.WriteString(event.Text)
		}
	})
	response, err := client.Respond(ctx, llm.Request{Model: llm.Model{ID: "fixture-model", ReasoningEffort: llm.ReasoningEffortMedium}, Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}}, Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "weather", Parameters: map[string]any{"type": "object"}}}}, llm.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "chat-1" || len(response.Output) != 2 || streamed.String() != "Hello" {
		t.Fatalf("response=%+v streamed=%q", response, streamed.String())
	}
	message := response.Output[0].Data.(llm.Message)
	call := response.Output[1].Data.(llm.ToolCall)
	if message.Text != "Hello" || call.Name != "weather" || call.Arguments != `{"city":"Paris"}` || call.CallID != "call-1" {
		t.Fatalf("decoded output message=%+v call=%+v", message, call)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 6 || response.Usage.CachedInputTokens != 3 || response.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage=%+v", response.Usage)
	}
}

func TestChatContentFilterIsRefusal(t *testing.T) {
	data := "data: {\"id\":\"filter\",\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n" + "data: [DONE]\n\n"
	response, err := decodeChatStream(context.Background(), strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if response.Stop != llm.StopRefused || response.Failure == nil || response.Failure.Code != "content_filter" {
		t.Fatalf("content filter response=%+v", response)
	}
}

func TestResponsesProtocolUsesGenericEndpointAndOmitsCodexFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer response-key" || r.Header.Get("ChatGPT-Account-ID") != "" {
			t.Errorf("request endpoint or headers unexpected: %s %v", r.URL.Path, r.Header)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, exists := request["prompt_cache_key"]; exists {
			t.Error("generic response endpoint received a provider-specific cache field")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[],\"logprobs\":[]}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}}\n\n")
	}))
	defer server.Close()
	client, err := NewClient(Provider{ID: "responses", Protocol: ProtocolResponses, BaseURL: server.URL + "/v1", APIKey: "response-key"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Respond(context.Background(), llm.Request{Model: llm.Model{ID: "fixture-model", ReasoningEffort: llm.ReasoningEffortMedium}, Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}}}, llm.RequestOptions{})
	if err != nil || len(response.Output) != 1 || response.Usage.InputTokens != 4 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestResponsesProtocolBoundsAggregateResponseBody(t *testing.T) {
	input := bytes.NewReader(make([]byte, maxResponseBytes+1))
	bounded := &boundedResponseBody{body: io.NopCloser(input), maxBytes: maxResponseBytes}
	data, err := io.ReadAll(bounded)
	if !errors.Is(err, errProviderResponseTooLarge) || len(data) != maxResponseBytes {
		t.Fatalf("bounded response bytes=%d err=%v", len(data), err)
	}
}

func TestResponsesProtocolPreservesRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(Provider{ID: "cancel-responses", Protocol: ProtocolResponses, BaseURL: server.URL, APIKey: "fixture-key"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Respond(ctx, llm.Request{Model: llm.Model{ID: "fixture"}, Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}}}, llm.RequestOptions{})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach fixture")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider request did not cancel")
	}
}

func TestChatCompletionsRequestCancellationAndErrorDoesNotEchoKey(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			close(started)
			select {
			case <-r.Context().Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
	}))
	defer server.Close()
	client, err := NewClient(Provider{ID: "cancel", Protocol: ProtocolChatCompletions, BaseURL: server.URL, APIKey: "private-key"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Respond(ctx, llm.Request{Model: llm.Model{ID: "fixture"}, Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "wait"}}}}, llm.RequestOptions{})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach fixture")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider request did not cancel")
	}
}

func TestChatCompletionHTTPErrorDoesNotReturnResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"leaked-key-in-this-body"}`)
	}))
	defer server.Close()
	client, err := NewClient(Provider{ID: "error", Protocol: ProtocolChatCompletions, BaseURL: server.URL, APIKey: "fixture-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Respond(context.Background(), llm.Request{Model: llm.Model{ID: "fixture"}}, llm.RequestOptions{})
	if err == nil || strings.Contains(err.Error(), "leaked-key") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe error=%v", err)
	}
}
