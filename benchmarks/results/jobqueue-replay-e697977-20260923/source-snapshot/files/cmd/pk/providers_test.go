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
