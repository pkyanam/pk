package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestCloudflareWorkersAIPresetBuildsScopedEndpointAndValidatesAccount(t *testing.T) {
	provider, err := NewPresetProviderWithAccountID("cloudflare-workers-ai", "cf", "private-token-fixture", "0123456789abcdef0123456789abcdef", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Protocol != ProtocolCloudflareWorkersAI || provider.BaseURL != "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1" || provider.DefaultEffort != "" || provider.SupportsReasoningEffort {
		t.Fatalf("Workers AI provider config = %+v", provider)
	}
	for _, bad := range []string{"", "not-an-account", "0123456789abcdef0123456789abcde/", "0123456789abcdef0123456789abcdeg"} {
		if _, err := NewPresetProviderWithAccountID("cloudflare-workers-ai", "cf", "token", bad, "", ""); err == nil {
			t.Errorf("accepted invalid Cloudflare account ID %q", bad)
		}
	}
	provider.BaseURL = "https://attacker.example/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1"
	if err := provider.Validate(); err == nil {
		t.Fatal("accepted a non-Cloudflare host for Workers AI protocol")
	}
}

func TestCloudflareWorkersAIModelDiscoveryMapsCallableNameAndCapabilities(t *testing.T) {
	baseURL := "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/models/search" {
			t.Errorf("discovery request = %s %s", request.Method, request.URL)
		}
		if request.URL.Query().Get("task") != "Text Generation" || request.URL.Query().Get("hide_experimental") != "false" || request.URL.Query().Get("page") != "1" {
			t.Errorf("discovery query = %v", request.URL.Query())
		}
		if request.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("auth header = %q", request.Header.Get("Authorization"))
		}
		return fakeResponse(http.StatusOK, `{"success":true,"result":[{"id":"uuid-1","name":"@cf/meta/tool-model","description":"model description","task":{"name":"Text Generation"},"properties":{"context_length":8192,"function_calling":true}},{"id":"uuid-2","name":"@cf/baai/embed","task":{"name":"Text Embeddings"}},{"id":"uuid-3","name":"@cf/meta/no-task"}]}`), nil
	})}
	models, err := listCloudflareWorkersAIModelsWithClient(context.Background(), baseURL, "fixture-token", client)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Model)
	for _, model := range models {
		byID[model.ID] = model
	}
	if len(byID) != 2 {
		t.Fatalf("discovered models = %+v", models)
	}
	model, ok := byID["@cf/meta/tool-model"]
	if !ok || model.Object != "workers_ai" || model.Task != "Text Generation" || model.Description != "model description" || len(model.Capabilities) != 1 || model.Capabilities[0] != "function_calling" || model.ContextTokens == nil || *model.ContextTokens != 8192 {
		t.Fatalf("discovered model metadata = %+v", model)
	}
	if _, ok := byID["uuid-1"]; ok {
		t.Fatal("exposed Cloudflare catalog UUID instead of callable model name")
	}
}

func TestCloudflareWorkersAIChatStreamingToolCallRoundTrip(t *testing.T) {
	provider := Provider{ID: "cf", Protocol: ProtocolCloudflareWorkersAI, BaseURL: "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1", APIKey: "fixture-token"}
	responses := []string{
		"data: {\"id\":\"resp-tool\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"list_files\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n",
		"data: {\"id\":\"resp-final\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
	}
	requests := 0
	client, err := NewClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	adapter := client.chat
	adapter.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != provider.BaseURL+"/chat/completions" || request.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Errorf("Workers AI request URL/auth = %q / %q", request.URL, request.Header.Get("Authorization"))
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if payload["model"] != "@cf/meta/agent-model" || payload["stream"] != true || (requests == 1 && payload["max_tokens"] != float64(256)) {
			t.Errorf("Workers AI request fields = %#v", payload)
		}
		if _, exists := payload["max_completion_tokens"]; exists {
			t.Error("Workers AI request used max_completion_tokens instead of max_tokens")
		}
		if requests == 1 {
			if _, ok := payload["tools"].([]any); !ok {
				t.Errorf("first request did not carry tool declarations: %#v", payload["tools"])
			}
		} else {
			messages := payload["messages"].([]any)
			foundCall, foundResult := false, false
			for _, raw := range messages {
				message := raw.(map[string]any)
				if message["role"] == "assistant" && message["tool_calls"] != nil {
					foundCall = true
				}
				if message["role"] == "tool" && message["tool_call_id"] == "call-1" {
					foundResult = true
				}
			}
			if !foundCall || !foundResult {
				t.Errorf("tool call/result were not round-tripped: %#v", messages)
			}
		}
		return fakeResponse(http.StatusOK, responses[requests-1]), nil
	})}
	first, err := adapter.Respond(context.Background(), llm.Request{
		Model: llm.Model{ID: "@cf/meta/agent-model", MaxOutputTokens: cloudflareTestTokenPtr(256)},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "inspect"}}},
		Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "list_files", Parameters: map[string]any{"type": "object"}}},
	}, llm.RequestOptions{})
	if err != nil || len(first.Output) != 1 || first.Output[0].Type != llm.ItemToolCall {
		t.Fatalf("tool response = %+v, %v", first, err)
	}
	call := first.Output[0].Data.(llm.ToolCall)
	second, err := adapter.Respond(context.Background(), llm.Request{
		Model: llm.Model{ID: "@cf/meta/agent-model"},
		Input: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "inspect"}},
			{Type: llm.ItemToolCall, Data: call},
			{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: call.CallID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "file.go"}}}},
		},
	}, llm.RequestOptions{})
	if err != nil || len(second.Output) != 1 || second.Output[0].Data.(llm.Message).Text != "done" || requests != 2 {
		t.Fatalf("final response=%+v err=%v requests=%d", second, err, requests)
	}
}

func cloudflareTestTokenPtr(value int64) *int64 { return &value }

func TestCloudflareWorkersAIToolUnsupportedErrorIsActionable(t *testing.T) {
	provider := Provider{ID: "cf", Protocol: ProtocolCloudflareWorkersAI, BaseURL: "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1"}
	adapter := newChatAdapter(provider, "", provider.BaseURL)
	adapter.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusBadRequest, `{"errors":[{"message":"private response body"}]}`), nil
	})}
	_, err := adapter.Respond(context.Background(), llm.Request{
		Model: llm.Model{ID: "@cf/meta/not-tools"}, Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "do_thing"}},
		Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "go"}}},
	}, llm.RequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "may not support function calling") || strings.Contains(err.Error(), "private response body") {
		t.Fatalf("tool error = %v", err)
	}
}
