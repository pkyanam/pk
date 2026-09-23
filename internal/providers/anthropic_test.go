package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestAnthropicMessagesStreamsToolsUsageSystemAndDiscovery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "fixture-secret" || r.Header.Get("anthropic-version") != anthropicVersion {
			t.Errorf("unexpected Anthropic headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/v1/models":
			if r.URL.Query().Get("after_id") == "" {
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-z","max_input_tokens":200000,"max_tokens":16000},{"id":"claude-a"},{"id":"claude-a"}],"has_more":true,"last_id":"claude-a"}`)
			} else {
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-b"}],"has_more":false}`)
			}
		case "/v1/messages":
			requests++
			var payload struct {
				Model     string           `json:"model"`
				MaxTokens int64            `json:"max_tokens"`
				Stream    bool             `json:"stream"`
				System    string           `json:"system"`
				Tools     []map[string]any `json:"tools"`
				Messages  []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if payload.Model != "claude-test" || payload.MaxTokens != 222 || !payload.Stream || payload.System != "system instructions" {
				t.Errorf("unexpected request metadata: %+v", payload)
			}
			if requests == 1 {
				if len(payload.Tools) != 1 || payload.Tools[0]["name"] != "weather" || len(payload.Messages) != 1 || payload.Messages[0].Role != "user" {
					t.Errorf("first request lacks native tools/user mapping: %+v", payload)
				}
			} else {
				if len(payload.Messages) < 3 {
					t.Errorf("tool loop messages missing: %+v", payload.Messages)
				}
				var assistant []map[string]any
				if err := json.Unmarshal(payload.Messages[1].Content, &assistant); err != nil || assistant[1]["type"] != "tool_use" {
					t.Errorf("tool call was not encoded as Anthropic tool_use: %s err=%v", payload.Messages[1].Content, err)
				}
				var result []map[string]any
				if err := json.Unmarshal(payload.Messages[2].Content, &result); err != nil || result[0]["type"] != "tool_result" {
					t.Errorf("tool result was not encoded as Anthropic tool_result: %s err=%v", payload.Messages[2].Content, err)
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			if requests == 1 {
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"usage\":{\"input_tokens\":12}}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Need weather\"}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu-1\",\"name\":\"weather\",\"input\":{}}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\n")
				_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":2}}\n\n")
			} else {
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-2\",\"usage\":{\"input_tokens\":20}}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Sunny\"}}\n\n")
				_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n")
			}
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := Provider{ID: "anthropic", Protocol: ProtocolAnthropic, BaseURL: server.URL + "/v1", APIKey: "fixture-secret", DefaultModel: "claude-test"}
	models, err := provider.Models(context.Background())
	if err != nil || len(models) != 3 || models[0].ID != "claude-a" || models[2].ID != "claude-z" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if models[2].InputTokens == nil || *models[2].InputTokens != 200_000 || models[2].OutputTokens == nil || *models[2].OutputTokens != 16_000 || models[2].LimitsSource != "provider_reported" {
		t.Fatalf("Anthropic model limits were not retained: %+v", models[2])
	}
	client, err := NewClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	model := llm.Model{ID: "claude-test", MaxOutputTokens: ptr(int64(222)), ReasoningEffort: llm.ReasoningEffortHigh}
	base := []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "system instructions"}}, {Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "weather in Paris?"}}}
	var streamed strings.Builder
	ctx := WithObserver(context.Background(), func(e Event) {
		if e.Kind == "assistant_delta" {
			streamed.WriteString(e.Text)
		}
	})
	first, err := client.Respond(ctx, llm.Request{Model: model, Input: base, Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}}}}, llm.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "msg-1" || first.Usage.InputTokens != 17 || first.Usage.OutputTokens != 7 || first.Usage.CachedInputTokens != 3 || first.Usage.CacheWriteInputTokens != 2 || streamed.String() != "Need weather" {
		t.Fatalf("first=%+v streamed=%q", first, streamed.String())
	}
	if !strings.Contains(string(first.Usage.Raw), `"input_tokens":12`) || !strings.Contains(string(first.Usage.Raw), `"cache_read_input_tokens":3`) || !strings.Contains(string(first.Usage.Raw), `"cache_creation_input_tokens":2`) {
		t.Fatalf("native raw usage was not merged: %s", first.Usage.Raw)
	}
	call := first.Output[len(first.Output)-1].Data.(llm.ToolCall)
	input := append(append([]llm.Item{}, base...), first.Output...)
	input = append(input, llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: call.CallID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "sunny 20C"}}}})
	second, err := client.Respond(context.Background(), llm.Request{Model: model, Input: input}, llm.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != "msg-2" || second.Output[0].Data.(llm.Message).Text != "Sunny" || second.Usage.InputTokens != 20 || second.Usage.OutputTokens != 4 || requests != 2 {
		t.Fatalf("second=%+v requests=%d", second, requests)
	}
}

func TestAnthropicEncodesParallelToolResultsAndImages(t *testing.T) {
	items := []llm.Item{
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-a", Name: "a", Arguments: `{"x":1}`}},
		{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-b", Name: "b", Arguments: `{}`}},
		{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call-a", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "ok"}}}},
		{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call-b", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultImage, Value: "data:image/png;base64,aGVsbG8="}}}},
	}
	messages, _, err := encodeAnthropicMessages(items)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected one assistant and one tool-result user message, got %+v", messages)
	}
	assistant := messages[0]["content"].([]map[string]any)
	results := messages[1]["content"].([]map[string]any)
	if len(assistant) != 2 || assistant[0]["type"] != "tool_use" || assistant[1]["type"] != "tool_use" || len(results) != 2 || results[0]["tool_use_id"] != "call-a" || results[1]["tool_use_id"] != "call-b" {
		t.Fatalf("parallel result mapping=%+v", messages)
	}
	imageBlock := results[1]["content"].([]map[string]any)[0]
	if imageBlock["type"] != "image" {
		t.Fatalf("image result was lost: %+v", imageBlock)
	}
	if _, _, err := encodeAnthropicMessages([]llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call", Name: "tool", Arguments: `{}`}}, {Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "call", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultImage, Value: "https://example.test/image.png"}}}}}); err == nil {
		t.Fatal("unsupported image URL was silently discarded")
	}
}

func TestAnthropicRejectsUnadvertisedReasoningEffort(t *testing.T) {
	provider := Provider{ID: "anthropic", Protocol: ProtocolAnthropic, BaseURL: "https://api.anthropic.com", SupportsReasoningEffort: true}
	if err := provider.Validate(); err == nil {
		t.Fatal("Anthropic generic reasoning-effort capability was accepted without a mapping")
	}
}

func TestAnthropicAutomaticCachingIsLimitedToOfficialHost(t *testing.T) {
	if !anthropicAutomaticCaching("https://api.anthropic.com/v1") {
		t.Fatal("official Anthropic host should get documented automatic cache breakpoint")
	}
	for _, endpoint := range []string{"https://proxy.example.test/v1", "https://notapi.anthropic.com/v1", "http://api.anthropic.com/v1"} {
		if anthropicAutomaticCaching(endpoint) {
			t.Errorf("unexpected automatic caching for %s", endpoint)
		}
	}
}

func TestAnthropicCancellationAndStreamErrors(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing version")
		}
		if r.Header.Get("x-api-key") != "key" {
			t.Error("missing API key")
		}
		if requests.Add(1) > 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"private body\"}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		flusher.Flush()
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	provider := Provider{ID: "anthropic", Protocol: ProtocolAnthropic, BaseURL: server.URL + "/v1", APIKey: "key"}
	client, err := NewClient(provider)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.Respond(ctx, llm.Request{Model: llm.Model{ID: "claude"}, Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}}}, llm.RequestOptions{})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach streaming handler")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request hung")
	}
	_, err = client.Respond(context.Background(), llm.Request{Model: llm.Model{ID: "claude"}, Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "hi"}}}}, llm.RequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "overloaded_error") || strings.Contains(err.Error(), "private body") {
		t.Fatalf("stream error leaked or missing: %v", err)
	}
}

func ptr(value int64) *int64 { return &value }
