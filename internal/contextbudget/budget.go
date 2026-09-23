// Package contextbudget resolves model token limits and provides explicitly
// approximate request-size estimates for context management.
package contextbudget

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/pkyanam/pk/internal/benchcontext"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type Source string

const (
	SourceProviderReported  Source = "provider_reported"
	SourceProviderCache     Source = "provider_cache"
	SourceCodexLocalCatalog Source = "codex_local_catalog"
	SourceOfficialCatalog   Source = "official_catalog"
	SourceUserOverride      Source = "user_override"
	SourceOperational       Source = "operational_fallback"
	SourceDerived           Source = "derived"
	SourceUnknown           Source = "unknown"
)

const (
	DefaultUnknownInputBudgetTokens int64 = 128_000
	DefaultOutputReserveTokens      int64 = 25_000
	DefaultSafetyMarginTokens       int64 = 4_096
	// Image payload tokenization depends on provider, dimensions, and detail
	// policy. This bounded allowance prevents one image from disabling all
	// later pressure estimates; it is deliberately marked very-low confidence.
	EstimatedTokensPerImage int64 = 16_384
)

// Limits are independent model capacities. Nil means unknown, never zero.
type Limits struct {
	ContextTokens *int64    `json:"context_tokens,omitempty"`
	InputTokens   *int64    `json:"input_tokens,omitempty"`
	OutputTokens  *int64    `json:"output_tokens,omitempty"`
	Source        Source    `json:"source,omitempty"`
	FetchedAt     time.Time `json:"fetched_at,omitempty"`
	// InputIncludesHeadroom marks an input allowance that already reserves
	// output/instructions overhead (for example Codex's effective window).
	InputIncludesHeadroom bool `json:"input_includes_headroom,omitempty"`
}

type ModelKey struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
}

type Override struct {
	ModelKey
	Limits
}

type Options struct {
	UnknownInputBudgetTokens *int64 `json:"unknown_input_budget_tokens,omitempty"`
	OutputReserveTokens      *int64 `json:"output_reserve_tokens,omitempty"`
	SafetyMarginTokens       *int64 `json:"safety_margin_tokens,omitempty"`
	ProviderBaseURL          string `json:"-"`
}

func DefaultOptions() Options {
	return Options{
		UnknownInputBudgetTokens: int64ptr(DefaultUnknownInputBudgetTokens),
		OutputReserveTokens:      int64ptr(DefaultOutputReserveTokens),
		SafetyMarginTokens:       int64ptr(DefaultSafetyMarginTokens),
	}
}

// Budget keeps published/provider limits separate from the operational input
// allowance used when those limits are unknown.
type Budget struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`

	ContextTokens         *int64     `json:"context_tokens,omitempty"`
	InputTokens           *int64     `json:"input_tokens,omitempty"`
	OutputTokens          *int64     `json:"output_tokens,omitempty"`
	ContextSource         Source     `json:"context_source"`
	InputSource           Source     `json:"input_source"`
	OutputSource          Source     `json:"output_source"`
	LimitsFetchedAt       *time.Time `json:"limits_fetched_at,omitempty"`
	InputIncludesHeadroom bool       `json:"input_includes_headroom,omitempty"`

	OperationalInputBudgetTokens  int64  `json:"operational_input_budget_tokens"`
	OperationalInputSource        Source `json:"operational_input_source"`
	OutputReserveTokens           int64  `json:"output_reserve_tokens"`
	ConfiguredOutputReserveTokens int64  `json:"configured_output_reserve_tokens"`
	SafetyMarginTokens            int64  `json:"safety_margin_tokens"`
}

func (budget Budget) HasPublishedLimit() bool {
	return budget.ContextTokens != nil || budget.InputTokens != nil
}

// Resolve combines exact provider/model catalog metadata, provider-reported
// limits, and per-field user overrides. Overrides never match by model name
// alone: the provider ID is part of the key.
func Resolve(providerID, modelID string, providerReported *Limits, overrides []Override, options Options) (Budget, error) {
	providerID, modelID = strings.TrimSpace(providerID), strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return Budget{}, errors.New("provider and model IDs are required for context budget resolution")
	}
	options, err := normalizeOptions(options)
	if err != nil {
		return Budget{}, err
	}

	limits := curatedLimits(ModelKey{ProviderID: providerID, ModelID: modelID}, options.ProviderBaseURL)
	if providerReported != nil {
		if err := validateLimits(*providerReported); err != nil {
			return Budget{}, fmt.Errorf("provider-reported limits: %w", err)
		}
		source := providerReported.Source
		if source == "" {
			source = SourceProviderReported
		}
		mergeLimits(&limits, *providerReported, source)
	}
	for _, override := range overrides {
		if !sameModelKey(override.ModelKey, providerID, modelID) {
			continue
		}
		if err := validateLimits(override.Limits); err != nil {
			return Budget{}, fmt.Errorf("override for %s/%s: %w", providerID, modelID, err)
		}
		mergeLimits(&limits, override.Limits, SourceUserOverride)
	}

	budget := Budget{
		ProviderID: providerID, ModelID: modelID,
		ContextTokens: clone(limits.ContextTokens), InputTokens: clone(limits.InputTokens), OutputTokens: clone(limits.OutputTokens),
		ContextSource: sourceOrUnknown(limits.ContextSource), InputSource: sourceOrUnknown(limits.InputSource), OutputSource: sourceOrUnknown(limits.OutputSource),
		InputIncludesHeadroom: limits.InputIncludesHeadroom,
		OutputReserveTokens:   *options.OutputReserveTokens, ConfiguredOutputReserveTokens: *options.OutputReserveTokens,
		SafetyMarginTokens: *options.SafetyMarginTokens,
	}
	if budget.OutputTokens != nil && *budget.OutputTokens < budget.OutputReserveTokens {
		budget.OutputReserveTokens = *budget.OutputTokens
	}
	if !limits.FetchedAt.IsZero() {
		fetchedAt := limits.FetchedAt
		budget.LimitsFetchedAt = &fetchedAt
	}
	if budget.ContextTokens == nil && budget.InputTokens == nil {
		budget.OperationalInputBudgetTokens = *options.UnknownInputBudgetTokens
		budget.OperationalInputSource = SourceOperational
		return budget, nil
	}
	usable := int64(math.MaxInt64)
	if budget.InputTokens != nil {
		usable = *budget.InputTokens
	}
	if budget.ContextTokens != nil {
		contextUsable := *budget.ContextTokens - budget.OutputReserveTokens
		if contextUsable < usable {
			usable = contextUsable
		}
	}
	usable -= *options.SafetyMarginTokens
	if usable < 0 {
		usable = 0
	}
	budget.OperationalInputBudgetTokens = usable
	budget.OperationalInputSource = SourceDerived
	return budget, nil
}

func normalizeOptions(options Options) (Options, error) {
	defaults := DefaultOptions()
	if options.UnknownInputBudgetTokens == nil {
		options.UnknownInputBudgetTokens = clone(defaults.UnknownInputBudgetTokens)
	}
	if options.OutputReserveTokens == nil {
		options.OutputReserveTokens = clone(defaults.OutputReserveTokens)
	}
	if options.SafetyMarginTokens == nil {
		options.SafetyMarginTokens = clone(defaults.SafetyMarginTokens)
	}
	if *options.UnknownInputBudgetTokens < 1 || *options.OutputReserveTokens < 0 || *options.SafetyMarginTokens < 0 {
		return Options{}, errors.New("context budget values must be positive (reserves may be zero)")
	}
	if options.ProviderBaseURL != "" {
		base, err := providers.NormalizeBaseURL(options.ProviderBaseURL)
		if err != nil {
			return Options{}, fmt.Errorf("provider base URL: %w", err)
		}
		options.ProviderBaseURL = base
	}
	return options, nil
}

func validateLimits(limits Limits) error {
	for name, value := range map[string]*int64{
		"context_tokens": limits.ContextTokens,
		"input_tokens":   limits.InputTokens,
		"output_tokens":  limits.OutputTokens,
	} {
		if value != nil && *value <= 0 {
			return fmt.Errorf("%s must be a positive token count when present", name)
		}
	}
	return nil
}

func mergeLimits(target *resolvedLimits, incoming Limits, source Source) {
	if !incoming.FetchedAt.IsZero() {
		target.FetchedAt = incoming.FetchedAt
	}
	if incoming.ContextTokens != nil {
		target.ContextTokens, target.ContextSource = clone(incoming.ContextTokens), source
	}
	if incoming.InputTokens != nil {
		target.InputTokens, target.InputSource = clone(incoming.InputTokens), source
		target.InputIncludesHeadroom = incoming.InputIncludesHeadroom
	}
	if incoming.OutputTokens != nil {
		target.OutputTokens, target.OutputSource = clone(incoming.OutputTokens), source
	}
}

type resolvedLimits struct {
	ContextTokens, InputTokens, OutputTokens *int64
	ContextSource, InputSource, OutputSource Source
	FetchedAt                                time.Time
	InputIncludesHeadroom                    bool
}

func curatedLimits(key ModelKey, baseURL string) resolvedLimits {
	// These values are from OpenAI's direct API model documentation only.
	// Do not reuse them for the native Codex/ChatGPT backend or other providers.
	if key.ProviderID == "openai" && baseURL == "https://api.openai.com/v1" {
		switch key.ModelID {
		case "gpt-6-luna", "gpt-6-sol", "gpt-6-astra":
			return resolvedLimits{
				ContextTokens: int64ptr(1_050_000), OutputTokens: int64ptr(128_000),
				ContextSource: SourceOfficialCatalog, OutputSource: SourceOfficialCatalog,
			}
		}
	}

	// Cloudflare's GLM-5.3-Flash model page publishes a 1,310,720-token
	// context window (verified 2026-09-23):
	// https://developers.cloudflare.com/workers-ai/models/glm-5.3-flash/
	// Some model-search catalog responses omit limit properties. Restrict this
	// fallback to the exact model on the official account-scoped Workers AI
	// endpoint. Fresh API metadata and user overrides are merged later and
	// therefore take precedence field-by-field.
	if key.ModelID == "@cf/zai-org/glm-5.3-flash" && isOfficialWorkersAIEndpoint(baseURL) {
		return resolvedLimits{ContextTokens: int64ptr(1_310_720), ContextSource: SourceOfficialCatalog}
	}
	return resolvedLimits{}
}

var workersAIAccountID = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

func isOfficialWorkersAIEndpoint(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "api.cloudflare.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return len(parts) == 6 && parts[0] == "client" && parts[1] == "v4" && parts[2] == "accounts" && workersAIAccountID.MatchString(parts[3]) && parts[4] == "ai" && parts[5] == "v1"
}

func sameModelKey(key ModelKey, providerID, modelID string) bool {
	return strings.TrimSpace(key.ProviderID) == providerID && strings.TrimSpace(key.ModelID) == modelID
}

func sourceOrUnknown(source Source) Source {
	if source == "" {
		return SourceUnknown
	}
	return source
}

func clone(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func int64ptr(value int64) *int64 { return &value }

type Estimate struct {
	Tokens                *int64 `json:"tokens,omitempty"`
	Method                string `json:"method"`
	Confidence            string `json:"confidence"`
	UnknownComponentBytes int64  `json:"unknown_component_bytes,omitempty"`
	Reason                string `json:"reason,omitempty"`
}

// EstimateRequest estimates input tokens from a request's text and JSON tool
// schemas. It is a low-confidence heuristic, not provider tokenization or a
// hard context-window safety check. Images use a clearly labeled fixed
// allowance because their encoded bytes do not predict provider tokenization.
func EstimateRequest(request llm.Request) Estimate {
	var bytes int64
	var opaqueBytes int64
	var imagePayloadBytes int64
	imageCount := int64(0)
	items := int64(0)
	for _, item := range request.Input {
		items++
		switch data := item.Data.(type) {
		case llm.Message:
			bytes += int64(len(data.Text)) + int64(len(data.Phase)) + int64(len(data.Role))
		case *llm.Message:
			if data == nil {
				return unavailableEstimate("nil message")
			}
			bytes += int64(len(data.Text)) + int64(len(data.Phase)) + int64(len(data.Role))
		case llm.ToolCall:
			bytes += int64(len(data.CallID) + len(data.Name) + len(data.Arguments))
		case *llm.ToolCall:
			if data == nil {
				return unavailableEstimate("nil tool call")
			}
			bytes += int64(len(data.CallID) + len(data.Name) + len(data.Arguments))
		case llm.ToolResult:
			bytes += int64(len(data.CallID))
			for _, output := range data.Output {
				if output.Kind == llm.ToolResultImage {
					imageCount++
					imagePayloadBytes += int64(len(output.Value))
					continue
				}
				if output.Kind != llm.ToolResultText {
					return unavailableEstimate("multimodal tool result")
				}
				bytes += int64(len(output.Value))
			}
		case *llm.ToolResult:
			if data == nil {
				return unavailableEstimate("nil tool result")
			}
			bytes += int64(len(data.CallID))
			for _, output := range data.Output {
				if output.Kind == llm.ToolResultImage {
					imageCount++
					imagePayloadBytes += int64(len(output.Value))
					continue
				}
				if output.Kind != llm.ToolResultText {
					return unavailableEstimate("multimodal tool result")
				}
				bytes += int64(len(output.Value))
			}
		case llm.Reasoning:
			opaqueBytes += int64(len(data.Raw))
		case *llm.Reasoning:
			if data == nil {
				return unavailableEstimate("nil reasoning item")
			}
			opaqueBytes += int64(len(data.Raw))
		default:
			return unavailableEstimate("unsupported input item")
		}
	}
	for _, tool := range request.Tools {
		items++
		bytes += int64(len(tool.Name) + len(tool.Description))
		encoded, err := json.Marshal(tool.Parameters)
		if err != nil {
			return unavailableEstimate("tool schema could not be encoded")
		}
		bytes += int64(len(encoded))
	}
	// Text and schemas use a practical byte-ratio heuristic. Opaque provider
	// reasoning bytes use a separate, deliberately conservative estimate and are
	// reported so callers can distinguish them from ordinary text.
	tokens := (bytes+2)/3 + (opaqueBytes+3)/4 + items*8 + imageCount*EstimatedTokensPerImage
	method, confidence := "utf8_bytes_div3_plus_8_tokens_per_item", "low"
	if opaqueBytes > 0 || imageCount > 0 {
		method = "utf8_bytes_div3_opaque_bytes_div4_plus_16384_tokens_per_image_plus_8_tokens_per_item"
		confidence = "very_low"
	}
	reason := ""
	if imageCount > 0 {
		reason = "images use a fixed 16384-token allowance each; provider image tokenization is unknown"
	}
	return Estimate{Tokens: &tokens, Method: method, Confidence: confidence, UnknownComponentBytes: opaqueBytes + imagePayloadBytes, Reason: reason}
}

func unavailableEstimate(reason string) Estimate {
	return Estimate{Method: "unavailable", Confidence: "none", Reason: reason}
}

// EstimateMeasuredRequest adapts pk's content-free request measurement. Its
// JSON byte size includes protocol structures and may include non-text payloads,
// so this remains a low-confidence heuristic rather than a safe upper bound.
func EstimateMeasuredRequest(record benchcontext.Record) Estimate {
	if record.InputValueBytes < 0 || record.InputItems < 0 || record.ToolSchemas.Items < 0 || record.SystemPrompt.Items < 0 || record.SystemPrompt.Bytes < 0 || record.ToolSchemas.Bytes < 0 || record.ToolCalls.Items < 0 || record.ToolCalls.Bytes < 0 || record.ToolResults.Items < 0 || record.ToolResults.Bytes < 0 || record.OtherInput.Items < 0 || record.OtherInput.Bytes < 0 {
		return unavailableEstimate("invalid request measurement")
	}
	var knownBytes int64
	for _, size := range record.MessageRoles {
		if size.Bytes < 0 || size.Items < 0 {
			return unavailableEstimate("invalid message measurement")
		}
		knownBytes += size.Bytes
	}
	knownBytes += record.ToolSchemas.Bytes + record.ToolCalls.Bytes
	opaqueBytes := record.ToolResults.Bytes + record.OtherInput.Bytes
	itemCount := record.InputItems + record.ToolSchemas.Items
	tokens := (knownBytes+2)/3 + (opaqueBytes+3)/4 + int64(itemCount)*8
	method, confidence := "json_value_bytes_div3_plus_8_tokens_per_item", "low"
	if opaqueBytes > 0 {
		method = "json_value_bytes_div3_opaque_json_bytes_div4_plus_8_tokens_per_item"
		confidence = "very_low"
	}
	return Estimate{Tokens: &tokens, Method: method, Confidence: confidence, UnknownComponentBytes: opaqueBytes}
}
