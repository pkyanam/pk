package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/providers"
)

func TestProviderCommandsStoreKeyFromStdinAndListRedactedSummary(t *testing.T) {
	store := providers.Store{Home: filepath.Join(t.TempDir(), "pk")}
	secret := "private-provider-key"
	var out, errOut strings.Builder
	code := runProviderCommandWithStore(context.Background(), []string{"add", "--id", "test", "--protocol", "chat_completions", "--base-url", "http://127.0.0.1:8080", "--api-key-stdin", "--model", "fixture"}, strings.NewReader(secret+"\n"), &out, &errOut, store)
	if code != 0 || !strings.Contains(out.String(), "Saved provider") {
		t.Fatalf("provider add code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	provider, err := store.Get("test")
	if err != nil || provider.APIKey != secret || provider.BaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("stored provider=%+v err=%v", provider, err)
	}
	out.Reset()
	errOut.Reset()
	code = runProviderCommandWithStore(context.Background(), []string{"list"}, strings.NewReader(""), &out, &errOut, store)
	if code != 0 || !strings.Contains(out.String(), "API key: stored privately") || strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
		t.Fatalf("provider list leaked or failed: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	info, err := os.Stat(filepath.Join(store.Home, "providers.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("provider secret store mode=%v err=%v", info, err)
	}
}

func TestProviderCLIAddsCloudflareWorkersAIPresetFromStdin(t *testing.T) {
	store := providers.Store{Home: filepath.Join(t.TempDir(), "pk")}
	secret := "cloudflare-cli-token-fixture"
	var out, errOut strings.Builder
	code := runProviderCommandWithStore(context.Background(), []string{"add", "--preset", "cloudflare-workers-ai", "--account-id", "0123456789abcdef0123456789abcdef", "--api-key-stdin"}, strings.NewReader(secret), &out, &errOut, store)
	if code != 0 || !strings.Contains(out.String(), "Saved provider") || strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
		t.Fatalf("Workers AI CLI setup code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	provider, err := store.Get("cloudflare-workers-ai")
	if err != nil || provider.APIKey != secret || provider.Protocol != providers.ProtocolCloudflareWorkersAI || !strings.Contains(provider.BaseURL, "/accounts/0123456789abcdef0123456789abcdef/ai/v1") || provider.DefaultEffort != "" {
		t.Fatalf("stored Workers AI provider=%+v err=%v", provider, err)
	}
	var invalidOut, invalidErr strings.Builder
	code = runProviderCommandWithStore(context.Background(), []string{"add", "--preset", "cloudflare-workers-ai", "--account-id", "bad", "--api-key-stdin"}, strings.NewReader("token"), &invalidOut, &invalidErr, store)
	if code == 0 || strings.Contains(invalidErr.String(), "token") {
		t.Fatalf("invalid Cloudflare account setup code=%d out=%q err=%q", code, invalidOut.String(), invalidErr.String())
	}
}

func TestProviderSetPreservesCredentialsOnlyForSameEndpoint(t *testing.T) {
	store := providers.Store{Home: filepath.Join(t.TempDir(), "pk")}
	if err := store.Put(providers.Provider{ID: "custom", Protocol: providers.ProtocolChatCompletions, BaseURL: "https://old.example.test/v1", APIKey: "old-secret", DefaultModel: "old-model"}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runProviderCommandWithStore(context.Background(), []string{"set", "--id", "custom", "--protocol", "chat_completions", "--base-url", "https://old.example.test/v1", "--model", "new-model"}, strings.NewReader(""), &out, &errOut, store)
	if code != 0 {
		t.Fatalf("same-endpoint set code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	got, err := store.Get("custom")
	if err != nil || got.APIKey != "old-secret" || got.DefaultModel != "new-model" {
		t.Fatalf("same-endpoint set did not preserve key/update model: %+v err=%v", got, err)
	}
	out.Reset()
	errOut.Reset()
	code = runProviderCommandWithStore(context.Background(), []string{"set", "--id", "custom", "--protocol", "chat_completions", "--base-url", "https://new.example.test/v1"}, strings.NewReader(""), &out, &errOut, store)
	if code == 0 || !strings.Contains(errOut.String(), "configure a new API key source") {
		t.Fatalf("changed-endpoint set unexpectedly succeeded: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	got, err = store.Get("custom")
	if err != nil || got.BaseURL != "https://old.example.test/v1" || got.APIKey != "old-secret" {
		t.Fatalf("failed endpoint change mutated stored provider: %+v err=%v", got, err)
	}
	out.Reset()
	errOut.Reset()
	code = runProviderCommandWithStore(context.Background(), []string{"add", "--id", "custom", "--protocol", "chat_completions", "--base-url", "https://old.example.test/v1", "--api-key-stdin"}, strings.NewReader("replacement"), &out, &errOut, store)
	if code == 0 || !strings.Contains(errOut.String(), "already exists") {
		t.Fatalf("duplicate add unexpectedly succeeded: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestProviderModelsCommandDiscoversEndpointModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	defer server.Close()
	store := providers.Store{Home: filepath.Join(t.TempDir(), "pk")}
	if err := store.Put(providers.Provider{ID: "local", Protocol: providers.ProtocolResponses, BaseURL: server.URL, DefaultModel: "model-a"}); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := runProviderCommandWithStore(context.Background(), []string{"models", "local"}, strings.NewReader(""), &out, &errOut, store)
	if code != 0 || out.String() != "model-a\nmodel-b\n" {
		t.Fatalf("provider models code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestProviderUsePersistsDefaultAndCanReturnToNative(t *testing.T) {
	store := providers.Store{Home: filepath.Join(t.TempDir(), "pk")}
	if err := store.Put(providers.Provider{ID: "custom", Protocol: providers.ProtocolResponses, BaseURL: "http://127.0.0.1:9000", DefaultModel: "model-x"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"use", "custom"}, {"use", "native"}} {
		var out, errOut bytes.Buffer
		if code := runProviderCommandWithStore(context.Background(), args, strings.NewReader(""), &out, &errOut, store); code != 0 {
			t.Fatalf("provider command %v code=%d out=%q err=%q", args, code, out.String(), errOut.String())
		}
	}
	defaultID, err := store.DefaultID()
	if err != nil || defaultID != "" {
		t.Fatalf("native default=%q err=%v", defaultID, err)
	}
	if code := runProviderCommandWithStore(context.Background(), []string{"use", "custom"}, strings.NewReader(""), io.Discard, io.Discard, store); code != 0 {
		t.Fatalf("select provider code=%d", code)
	}
	defaultID, err = store.DefaultID()
	if err != nil || defaultID != "custom" {
		t.Fatalf("custom default=%q err=%v", defaultID, err)
	}
}
