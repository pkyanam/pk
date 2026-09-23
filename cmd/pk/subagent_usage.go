package main

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/pkyanam/pk/internal/subagents"
)

// synchronizedOutput serializes parent runner output and asynchronous child
// accounting events into whole writes on the same stream.
type synchronizedOutput struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

type childUsageRecord struct {
	ResponseID                     string `json:"response_id"`
	InputTokens                    *int64 `json:"input_tokens"`
	OutputTokens                   *int64 `json:"output_tokens"`
	ReasoningTokens                *int64 `json:"reasoning_tokens"`
	CachedInputTokens              *int64 `json:"cached_input_tokens"`
	CachedInputTokensAvailable     *bool  `json:"cached_input_tokens_available"`
	CacheWriteInputTokens          *int64 `json:"cache_write_input_tokens"`
	CacheWriteInputTokensAvailable *bool  `json:"cache_write_input_tokens_available"`
	UsageAvailable                 *bool  `json:"usage_available"`
}

// subagentJSONLForwarder exposes only lifecycle and accounting metadata. Child
// prompts, assistant text, tool arguments, model names, and raw payloads stay
// in their own runner stream and are never copied to the parent CLI output.
func subagentJSONLForwarder(out io.Writer, enabled bool) func(subagents.Event) {
	if !enabled || out == nil {
		return nil
	}
	var mu sync.Mutex
	started := make(map[string]struct{})
	responses := make(map[string]struct{})
	unavailable := make(map[string]struct{})
	emitUnavailable := func(childID, responseID string) {
		key := childID + "\x00" + responseID
		mu.Lock()
		if _, exists := unavailable[key]; exists {
			mu.Unlock()
			return
		}
		unavailable[key] = struct{}{}
		mu.Unlock()
		line := map[string]any{"type": "subagent_accounting_unavailable", "child_id": childID}
		if responseID != "" && len(responseID) <= 256 {
			line["response_id"] = responseID
		}
		_ = writeSubagentJSONLine(out, line)
	}
	return func(event subagents.Event) {
		switch {
		case event.Type == "subagent" && event.State == "running":
			mu.Lock()
			if _, exists := started[event.ChildID]; exists {
				mu.Unlock()
				return
			}
			started[event.ChildID] = struct{}{}
			mu.Unlock()
			_ = writeSubagentJSONLine(out, map[string]any{"type": "subagent_started", "child_id": event.ChildID})
		case event.Type == "usage":
			var usage childUsageRecord
			if len(event.Payload) == 0 || json.Unmarshal(event.Payload, &usage) != nil || !validChildUsage(usage) {
				var identity struct {
					ResponseID string `json:"response_id"`
				}
				_ = json.Unmarshal(event.Payload, &identity)
				emitUnavailable(event.ChildID, identity.ResponseID)
				return
			}
			key := event.ChildID + "\x00" + usage.ResponseID
			mu.Lock()
			if _, exists := responses[key]; exists {
				mu.Unlock()
				return
			}
			responses[key] = struct{}{}
			mu.Unlock()
			line := map[string]any{
				"type": "subagent_usage", "child_id": event.ChildID, "response_id": usage.ResponseID,
				"usage_available": *usage.UsageAvailable,
			}
			if usage.InputTokens != nil {
				line["input_tokens"] = *usage.InputTokens
			}
			if usage.OutputTokens != nil {
				line["output_tokens"] = *usage.OutputTokens
			}
			if usage.ReasoningTokens != nil {
				line["reasoning_tokens"] = *usage.ReasoningTokens
			}
			if usage.CachedInputTokens != nil {
				line["cached_input_tokens"] = *usage.CachedInputTokens
			}
			if usage.CachedInputTokensAvailable != nil {
				line["cached_input_tokens_available"] = *usage.CachedInputTokensAvailable
			}
			if usage.CacheWriteInputTokens != nil {
				line["cache_write_input_tokens"] = *usage.CacheWriteInputTokens
			}
			if usage.CacheWriteInputTokensAvailable != nil {
				line["cache_write_input_tokens_available"] = *usage.CacheWriteInputTokensAvailable
			}
			_ = writeSubagentJSONLine(out, line)
		}
	}
}

func writeSubagentJSONLine(out io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = out.Write(encoded)
	return err
}

func validChildUsage(usage childUsageRecord) bool {
	if usage.ResponseID == "" || len(usage.ResponseID) > 256 || usage.UsageAvailable == nil {
		return false
	}
	for _, count := range []*int64{usage.InputTokens, usage.OutputTokens, usage.ReasoningTokens, usage.CachedInputTokens, usage.CacheWriteInputTokens} {
		if count != nil && *count < 0 {
			return false
		}
	}
	return true
}

func synchronizedJSONLOutput(out io.Writer) io.Writer {
	if out == nil {
		return io.Discard
	}
	if _, ok := out.(*synchronizedOutput); ok {
		return out
	}
	return &synchronizedOutput{w: out}
}
