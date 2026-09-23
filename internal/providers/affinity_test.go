package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const affinityResponse = "data: {\"id\":\"r\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func TestWorkersAISessionAffinityIsHashedStableAndProtocolScoped(t *testing.T) {
	const rawKey = "stable-session-id-do-not-send"
	first := captureChatAffinity(t, ProtocolCloudflareWorkersAI, rawKey)
	second := captureChatAffinity(t, ProtocolCloudflareWorkersAI, rawKey)
	if first.affinity == "" || first.affinity != second.affinity {
		t.Fatalf("recreated clients did not retain affinity: first=%q second=%q", first.affinity, second.affinity)
	}
	digest := sha256.Sum256([]byte(rawKey))
	want := "pk-" + hex.EncodeToString(digest[:])
	if first.affinity != want {
		t.Fatalf("affinity=%q want prefixed SHA-256 %q", first.affinity, want)
	}
	if strings.Contains(first.allRequestData, rawKey) {
		t.Fatal("raw cache key was exposed in the outgoing request")
	}

	different := captureChatAffinity(t, ProtocolCloudflareWorkersAI, "another-session-id")
	if different.affinity == first.affinity {
		t.Fatalf("different sessions shared affinity %q", first.affinity)
	}

	empty := captureChatAffinity(t, ProtocolCloudflareWorkersAI, "")
	if empty.affinity != "" {
		t.Fatalf("empty cache key sent affinity header %q", empty.affinity)
	}

	generic := captureChatAffinity(t, ProtocolChatCompletions, rawKey)
	if generic.affinity != "" {
		t.Fatalf("generic Chat Completions provider sent Workers AI affinity %q", generic.affinity)
	}
	if strings.Contains(generic.allRequestData, rawKey) {
		t.Fatal("generic provider exposed the raw cache key")
	}
}

type capturedAffinityRequest struct {
	affinity       string
	allRequestData string
}

func captureChatAffinity(t *testing.T, protocol Protocol, cacheKey string) capturedAffinityRequest {
	t.Helper()
	provider := Provider{ID: "fixture", Protocol: protocol, BaseURL: "https://provider.example/v1"}
	adapter := newChatAdapter(provider, "fixture-api-token", provider.BaseURL)
	defer adapter.Close()
	var captured capturedAffinityRequest
	adapter.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured.affinity = request.Header.Get("x-session-affinity")
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		captured.allRequestData = request.Header.Get("x-session-affinity") + "\n" + request.Header.Get("Authorization") + "\n" + string(body)
		return fakeResponse(http.StatusOK, affinityResponse), nil
	})}
	_, err := adapter.Respond(context.Background(), llm.Request{
		Model: llm.Model{ID: "fixture-model"},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hello"}}},
	}, llm.RequestOptions{CacheKey: cacheKey})
	if err != nil {
		t.Fatalf("Respond(%s): %v", protocol, err)
	}
	return captured
}
