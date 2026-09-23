// Package benchcontext captures request-shape metadata for opt-in benchmarks.
// It never stores request values or provider error text.
package benchcontext

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sync"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type Size struct {
	Items int   `json:"items"`
	Bytes int64 `json:"bytes"`
}

type Usage struct {
	InputTokens          int64 `json:"input_tokens"`
	InputAvailable       bool  `json:"input_tokens_available"`
	OutputTokens         int64 `json:"output_tokens"`
	OutputAvailable      bool  `json:"output_tokens_available"`
	CachedInputTokens    int64 `json:"cached_input_tokens"`
	CachedInputAvailable bool  `json:"cached_input_tokens_available"`
	CacheWriteTokens     int64 `json:"cache_write_input_tokens"`
	CacheWriteAvailable  bool  `json:"cache_write_input_tokens_available"`
}

type Record struct {
	Type            string          `json:"type"`
	RequestOrdinal  int             `json:"request_ordinal"`
	ResponseID      string          `json:"response_id,omitempty"`
	RequestFailed   bool            `json:"request_failed"`
	SystemPrompt    Size            `json:"system_prompt"`
	ToolSchemas     Size            `json:"tool_schemas"`
	MessageRoles    map[string]Size `json:"message_roles"`
	ToolCalls       Size            `json:"tool_calls"`
	ToolResults     Size            `json:"tool_results"`
	OtherInput      Size            `json:"other_input"`
	InputItems      int             `json:"input_items"`
	InputValueBytes int64           `json:"input_value_bytes"`
	Usage           Usage           `json:"usage"`
}

// Adapter decorates an llm.Adapter and writes one content-free record for each
// request. Bytes count JSON-encoded values in the harness Request, not wire
// envelope punctuation or estimated tokenizer tokens.
type Adapter struct {
	Next   llm.Adapter
	Output io.Writer
	mu     sync.Mutex
	count  int
}

func (adapter *Adapter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	if adapter == nil || adapter.Next == nil || adapter.Output == nil {
		return llm.Response{}, errors.New("benchmark context adapter is not configured")
	}
	record, err := MeasureRequest(request)
	if err != nil {
		return llm.Response{}, err
	}
	adapter.mu.Lock()
	adapter.count++
	record.RequestOrdinal = adapter.count
	adapter.mu.Unlock()
	response, requestErr := adapter.Next.Respond(ctx, request, options)
	record.ResponseID = response.ID
	record.RequestFailed = requestErr != nil
	record.Usage = measureUsage(response.Usage)
	data, err := json.Marshal(record)
	if err != nil {
		return llm.Response{}, err
	}
	data = append(data, '\n')
	adapter.mu.Lock()
	n, writeErr := adapter.Output.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	adapter.mu.Unlock()
	if writeErr != nil {
		return llm.Response{}, errors.New("write benchmark context metrics")
	}
	return response, requestErr
}

func MeasureRequest(request llm.Request) (Record, error) {
	record := Record{Type: "benchmark_context", MessageRoles: make(map[string]Size)}
	for _, item := range request.Input {
		value, err := json.Marshal(item.Data)
		if err != nil {
			return Record{}, errors.New("measure benchmark request item")
		}
		size := Size{Items: 1, Bytes: int64(len(value))}
		record.InputItems++
		record.InputValueBytes += int64(len(value))
		switch item.Type {
		case llm.ItemMessage:
			message, ok := item.Data.(llm.Message)
			if !ok {
				return Record{}, errors.New("measure benchmark message item")
			}
			role := safeRole(string(message.Role))
			roleSize := record.MessageRoles[role]
			roleSize.Items += size.Items
			roleSize.Bytes += size.Bytes
			record.MessageRoles[role] = roleSize
			if message.Role == llm.RoleSystem {
				record.SystemPrompt.Items++
				record.SystemPrompt.Bytes += size.Bytes
			}
		case llm.ItemToolCall:
			record.ToolCalls.Items++
			record.ToolCalls.Bytes += size.Bytes
		case llm.ItemToolResult:
			record.ToolResults.Items++
			record.ToolResults.Bytes += size.Bytes
		default:
			record.OtherInput.Items++
			record.OtherInput.Bytes += size.Bytes
		}
	}
	for _, tool := range request.Tools {
		value, err := json.Marshal(tool)
		if err != nil {
			return Record{}, errors.New("measure benchmark tool schema")
		}
		record.ToolSchemas.Items++
		record.ToolSchemas.Bytes += int64(len(value))
	}
	return record, nil
}

func measureUsage(usage llm.Usage) Usage {
	raw := map[string]any{}
	if len(usage.Raw) > 0 {
		_ = json.Unmarshal(usage.Raw, &raw)
	}
	inputKnown := usage.InputTokens != 0 || hasNumeric(raw, "input_tokens", "prompt_tokens", "InputTokens")
	outputKnown := usage.OutputTokens != 0 || hasNumeric(raw, "output_tokens", "completion_tokens", "OutputTokens")
	cachedKnown := usage.CachedInputTokens != 0 || hasNumeric(raw, "cached_input_tokens", "cached_tokens", "CachedInputTokens")
	writeKnown := usage.CacheWriteInputTokens != 0 || hasNumeric(raw, "cache_write_input_tokens", "cache_write_tokens", "CacheWriteInputTokens")
	return Usage{
		InputTokens: usage.InputTokens, InputAvailable: inputKnown,
		OutputTokens: usage.OutputTokens, OutputAvailable: outputKnown,
		CachedInputTokens: usage.CachedInputTokens, CachedInputAvailable: cachedKnown,
		CacheWriteTokens: usage.CacheWriteInputTokens, CacheWriteAvailable: writeKnown,
	}
}

func hasNumeric(value any, names ...string) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			for _, name := range names {
				if key == name {
					number, ok := child.(float64)
					return ok && number >= 0 && number <= math.MaxInt64 && math.Trunc(number) == number && !math.IsNaN(number) && !math.IsInf(number, 0)
				}
			}
			if hasNumeric(child, names...) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if hasNumeric(child, names...) {
				return true
			}
		}
	}
	return false
}

func safeRole(role string) string {
	switch role {
	case "system", "developer", "user", "assistant", "tool":
		return role
	default:
		return "unknown"
	}
}
