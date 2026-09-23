package benchcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type fixtureAdapter struct {
	response llm.Response
	err      error
}

func (adapter fixtureAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	return adapter.response, adapter.err
}

func TestAdapterWritesOnlyComponentCountsAndCorrelatesUsage(t *testing.T) {
	request := llm.Request{
		Input: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "PRIVATE_SYSTEM_SENTINEL"}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "PRIVATE_USER_SENTINEL"}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "PRIVATE_ASSISTANT_SENTINEL"}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "private-call-id", Name: "private-tool", Arguments: `{"secret":"PRIVATE_TOOL_SENTINEL"}`}},
			{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "private-call-id", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "PRIVATE_RESULT_SENTINEL"}}}},
			{Type: llm.ItemReasoning, Data: llm.Reasoning{Summary: []string{"PRIVATE_REASONING_SENTINEL"}}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.Role("PRIVATE_UNKNOWN_ROLE_SENTINEL"), Text: "other"}},
		},
		Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "PRIVATE_SCHEMA_SENTINEL", Description: "PRIVATE_DESCRIPTION_SENTINEL", Parameters: map[string]any{"type": "object"}}},
	}
	response := llm.Response{ID: "response-fixture", Usage: llm.Usage{InputTokens: 12, CachedInputTokens: 3, OutputTokens: 7, Raw: json.RawMessage(`{"input_tokens":12,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}`)}}
	var output bytes.Buffer
	adapter := &Adapter{Next: fixtureAdapter{response: response}, Output: &output}
	got, err := adapter.Respond(context.Background(), request, llm.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != response.ID {
		t.Fatalf("response ID=%q, want %q", got.ID, response.ID)
	}
	line := strings.TrimSpace(output.String())
	var record Record
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	if record.Type != "benchmark_context" || record.RequestOrdinal != 1 || record.ResponseID != response.ID || record.RequestFailed {
		t.Fatalf("record metadata=%+v", record)
	}
	if record.InputItems != 7 || record.SystemPrompt.Items != 1 || record.ToolSchemas.Items != 1 || record.ToolCalls.Items != 1 || record.ToolResults.Items != 1 || record.OtherInput.Items != 1 {
		t.Fatalf("record item counts=%+v", record)
	}
	if record.MessageRoles["system"].Items != 1 || record.MessageRoles["user"].Items != 1 || record.MessageRoles["assistant"].Items != 1 {
		t.Fatalf("message role counts=%+v", record.MessageRoles)
	}
	if record.MessageRoles["unknown"].Items != 1 {
		t.Fatalf("unrecognized role was not bucketed: %+v", record.MessageRoles)
	}
	if !record.Usage.InputAvailable || !record.Usage.OutputAvailable || !record.Usage.CachedInputAvailable || record.Usage.InputTokens != 12 || record.Usage.CachedInputTokens != 3 || record.Usage.OutputTokens != 7 {
		t.Fatalf("usage=%+v", record.Usage)
	}
	for _, secret := range []string{"PRIVATE_", "private-call-id", "private-tool", "secret"} {
		if strings.Contains(line, secret) {
			t.Fatalf("metrics record leaked %q: %s", secret, line)
		}
	}
	expectBytes := func(value any) int64 {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return int64(len(encoded))
	}
	if record.SystemPrompt.Bytes != expectBytes(request.Input[0].Data) || record.ToolSchemas.Bytes != expectBytes(request.Tools[0]) || record.ToolResults.Bytes != expectBytes(request.Input[4].Data) {
		t.Fatalf("component serialized bytes=%+v", record)
	}
	var total int64
	for _, item := range request.Input {
		total += expectBytes(item.Data)
	}
	if record.InputValueBytes != total || record.InputItems != len(request.Input) {
		t.Fatalf("aggregate serialized bytes=%d items=%d; want %d/%d", record.InputValueBytes, record.InputItems, total, len(request.Input))
	}
}

func TestAdapterFailureAndMalformedUsageDoNotLeakOrClaimAvailability(t *testing.T) {
	request := llm.Request{Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.Role("unknown-secret-role"), Text: "PRIVATE_REQUEST"}}}}
	response := llm.Response{Usage: llm.Usage{Raw: json.RawMessage(`{"input_tokens":null,"output_tokens":"12","cached_tokens":null}`)}}
	var output bytes.Buffer
	adapter := &Adapter{Next: fixtureAdapter{response: response, err: errors.New("PRIVATE_RAW_ERROR")}, Output: &output}
	_, err := adapter.Respond(context.Background(), request, llm.RequestOptions{})
	if err == nil || err.Error() != "PRIVATE_RAW_ERROR" {
		t.Fatalf("adapter error=%v", err)
	}
	line := output.String()
	for _, secret := range []string{"PRIVATE_REQUEST", "PRIVATE_RAW_ERROR", "unknown-secret-role"} {
		if strings.Contains(line, secret) {
			t.Fatalf("metrics leaked %q: %s", secret, line)
		}
	}
	var record Record
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatal(err)
	}
	if !record.RequestFailed || record.Usage.InputAvailable || record.Usage.OutputAvailable || record.Usage.CachedInputAvailable {
		t.Fatalf("failed request or malformed usage reported incorrectly: %+v", record)
	}
}
