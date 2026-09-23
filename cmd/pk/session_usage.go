package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type sessionUsageCoverage struct {
	InputResponses       int `json:"input_responses"`
	OutputResponses      int `json:"output_responses"`
	CachedInputResponses int `json:"cached_input_responses"`
	UncachedResponses    int `json:"uncached_input_responses"`
}

type sessionUsageSummary struct {
	SessionID           string               `json:"session_id"`
	ResponseCount       int                  `json:"response_count"`
	InputTokens         *int64               `json:"input_tokens"`
	OutputTokens        *int64               `json:"output_tokens"`
	CachedInputTokens   *int64               `json:"cached_input_tokens"`
	UncachedInputTokens *int64               `json:"uncached_input_tokens"`
	Coverage            sessionUsageCoverage `json:"coverage"`
}

func readSessionUsage(ctx context.Context, store *localfile.Store, id string) (sessionUsageSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	summary := sessionUsageSummary{SessionID: id}
	var inputSum, outputSum, cachedSum, uncachedSum int64
	var inputSeen, outputSeen, cachedSeen, uncachedSeen bool
	var inputOverflow, outputOverflow, cachedOverflow, uncachedOverflow bool
	page, err := store.Items(ctx, session.ID(id), sessionstore.BeforeFirst, int(^uint(0)>>1))
	if err != nil {
		return sessionUsageSummary{}, err
	}
	for i, item := range page.Items {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return sessionUsageSummary{}, err
			}
		}
		if item.Kind != sessionstore.ItemModelResponse {
			continue
		}
		response, ok := item.Data.(sessionstore.ModelResponse)
		if !ok {
			continue
		}
		summary.ResponseCount++
		usage := response.Response.Usage
		input, inputOK := usageCounter(usage, "input_tokens", usage.InputTokens)
		output, outputOK := usageCounter(usage, "output_tokens", usage.OutputTokens)
		cached, cachedOK := cachedUsageCounter(usage)
		if inputOK {
			inputSeen = true
			inputOverflow = inputOverflow || !addUsage(&inputSum, input)
			summary.Coverage.InputResponses++
		}
		if outputOK {
			outputSeen = true
			outputOverflow = outputOverflow || !addUsage(&outputSum, output)
			summary.Coverage.OutputResponses++
		}
		if cachedOK {
			cachedSeen = true
			cachedOverflow = cachedOverflow || !addUsage(&cachedSum, cached)
			summary.Coverage.CachedInputResponses++
		}
		if inputOK && cachedOK && cached <= input {
			uncachedSeen = true
			uncachedOverflow = uncachedOverflow || !addUsage(&uncachedSum, input-cached)
			summary.Coverage.UncachedResponses++
		}
	}
	if inputSeen && !inputOverflow {
		summary.InputTokens = int64Pointer(inputSum)
	}
	if outputSeen && !outputOverflow {
		summary.OutputTokens = int64Pointer(outputSum)
	}
	if cachedSeen && !cachedOverflow {
		summary.CachedInputTokens = int64Pointer(cachedSum)
	}
	if uncachedSeen && !uncachedOverflow {
		summary.UncachedInputTokens = int64Pointer(uncachedSum)
	}
	return summary, nil
}

func usageCounter(usage llm.Usage, field string, typed int64) (int64, bool) {
	aliases := []string{field}
	switch field {
	case "input_tokens":
		aliases = append(aliases, "prompt_tokens")
	case "output_tokens":
		aliases = append(aliases, "completion_tokens")
	}
	if value, valid, present := rawUsageInt(usage.Raw, aliases...); present {
		return value, valid && value >= 0
	}
	return typed, typed > 0
}

func cachedUsageCounter(usage llm.Usage) (int64, bool) {
	if len(usage.Raw) > 0 {
		for _, field := range []string{"input_tokens_details", "prompt_tokens_details"} {
			var raw map[string]json.RawMessage
			if json.Unmarshal(usage.Raw, &raw) != nil {
				break
			}
			if detailBytes, exists := raw[field]; exists {
				var details map[string]json.RawMessage
				if json.Unmarshal(detailBytes, &details) != nil {
					return 0, false
				}
				if valueBytes, exists := details["cached_tokens"]; exists {
					value, valid := parseUsageCounter(valueBytes)
					return value, valid && value >= 0
				}
			}
		}
	}
	return usage.CachedInputTokens, usage.CachedInputTokens > 0
}

func rawUsageInt(raw []byte, fields ...string) (int64, bool, bool) {
	if len(raw) == 0 {
		return 0, false, false
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return 0, false, false
	}
	for _, field := range fields {
		value, exists := values[field]
		if !exists {
			continue
		}
		count, valid := parseUsageCounter(value)
		return count, valid, true
	}
	return 0, false, false
}

func parseUsageCounter(value json.RawMessage) (int64, bool) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return 0, false
	}
	var count int64
	if json.Unmarshal(value, &count) != nil {
		return 0, false
	}
	return count, true
}

func addUsage(total *int64, value int64) bool {
	if value < 0 || *total > math.MaxInt64-value {
		return false
	}
	*total += value
	return true
}

func int64Pointer(value int64) *int64 { return &value }
