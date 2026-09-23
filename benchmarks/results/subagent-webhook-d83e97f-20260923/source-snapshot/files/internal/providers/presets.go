package providers

import (
	"fmt"
	"strings"

	"github.com/pkyanam/pk/internal/config"
)

// Preset describes a verified provider endpoint. It only carries public
// connection metadata; credentials are supplied separately by the user.
type Preset struct {
	ID                      string   `json:"id"`
	Label                   string   `json:"label"`
	APIStyle                string   `json:"api_style"`
	Protocol                Protocol `json:"protocol"`
	BaseURL                 string   `json:"base_url"`
	APIKeyEnv               string   `json:"api_key_env"`
	SupportsReasoningEffort bool     `json:"supports_reasoning_effort"`
	DocsURL                 string   `json:"docs_url"`
	CompatibilityNote       string   `json:"compatibility_note"`
}

const openAICompatibilityNote = "OpenAI-style API compatibility; available models and supported features vary by provider and model."
const openAIStyle = "openai_compatible"

var presetCatalog = []Preset{
	{
		ID: "anthropic", Label: "Anthropic", APIStyle: "anthropic_messages", Protocol: ProtocolAnthropic,
		BaseURL: "https://api.anthropic.com/v1", APIKeyEnv: "ANTHROPIC_API_KEY",
		DocsURL: "https://docs.anthropic.com/en/api/messages", CompatibilityNote: "Native Anthropic Messages API; this is not an OpenAI-compatible endpoint.",
	},
	{
		ID: "cerebras", Label: "Cerebras", APIStyle: openAIStyle, Protocol: ProtocolChatCompletions,
		BaseURL: "https://api.cerebras.ai/v1", APIKeyEnv: "CEREBRAS_API_KEY",
		DocsURL: "https://inference-docs.cerebras.ai/", CompatibilityNote: openAICompatibilityNote,
	},
	{
		ID: "gemini", Label: "Google Gemini", APIStyle: openAIStyle, Protocol: ProtocolChatCompletions,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", APIKeyEnv: "GEMINI_API_KEY",
		DocsURL: "https://ai.google.dev/gemini-api/docs/openai", CompatibilityNote: "Google documents this as an OpenAI-compatible surface; use its native SDK for full Gemini feature parity.",
	},
	{
		ID: "groq", Label: "Groq", APIStyle: openAIStyle, Protocol: ProtocolChatCompletions,
		BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY",
		DocsURL: "https://console.groq.com/docs/openai", CompatibilityNote: openAICompatibilityNote,
	},
	{
		ID: "mistral", Label: "Mistral", APIStyle: openAIStyle, Protocol: ProtocolChatCompletions,
		BaseURL: "https://api.mistral.ai/v1", APIKeyEnv: "MISTRAL_API_KEY",
		DocsURL: "https://docs.mistral.ai/capabilities/completion/", CompatibilityNote: openAICompatibilityNote,
	},
	{
		ID: "openai", Label: "OpenAI API", APIStyle: openAIStyle, Protocol: ProtocolResponses,
		BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY",
		DocsURL: "https://platform.openai.com/docs/api-reference/responses", CompatibilityNote: "Uses the OpenAI Responses API; this is separate from native ChatGPT login.",
	},
	{
		ID: "openrouter", Label: "OpenRouter", APIStyle: openAIStyle, Protocol: ProtocolChatCompletions,
		BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY",
		DocsURL: "https://openrouter.ai/docs/quickstart", CompatibilityNote: "Uses OpenRouter model IDs; advertised models, availability, and features depend on the selected model and account.",
	},
	{
		ID: "together", Label: "Together AI", APIStyle: openAIStyle, Protocol: ProtocolChatCompletions,
		BaseURL: "https://api.together.ai/v1", APIKeyEnv: "TOGETHER_API_KEY",
		DocsURL: "https://docs.together.ai/docs/openai-api-compatibility", CompatibilityNote: "Uses Together model IDs; advertised models, availability, and features depend on the selected model and account.",
	},
	{
		ID: "xai", Label: "xAI", APIStyle: openAIStyle, Protocol: ProtocolResponses,
		BaseURL: "https://api.x.ai/v1", APIKeyEnv: "XAI_API_KEY",
		DocsURL: "https://docs.x.ai/developers/model-capabilities/text/generate-text", CompatibilityNote: openAICompatibilityNote,
	},
}

// Presets returns a copy of the stable preset catalog. No model IDs are baked
// in; callers should discover models using the user's configured API key.
func Presets() []Preset {
	return append([]Preset(nil), presetCatalog...)
}

// PresetByID resolves a preset without performing network requests.
func PresetByID(id string) (Preset, bool) {
	for _, preset := range presetCatalog {
		if preset.ID == id {
			return preset, true
		}
	}
	return Preset{}, false
}

// NewPresetProvider constructs a provider from a catalog entry. An empty
// provider ID defaults to the preset's ID; model remains empty for live
// discovery, and effort uses pk's normal configured default.
func NewPresetProvider(presetID, providerID, apiKey, model, effort string) (Provider, error) {
	preset, ok := PresetByID(presetID)
	if !ok {
		return Provider{}, fmt.Errorf("unknown provider preset %q", presetID)
	}
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		providerID = preset.ID
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || len(apiKey) > 4096 {
		return Provider{}, fmt.Errorf("API key is required and must be at most 4096 bytes")
	}
	defaultEffort := strings.ToLower(strings.TrimSpace(effort))
	if defaultEffort == "" && preset.Protocol != ProtocolAnthropic {
		defaultEffort = config.DefaultEffort
	}
	provider := Provider{
		ID: providerID, Protocol: preset.Protocol, BaseURL: preset.BaseURL,
		APIKey: apiKey, DefaultModel: strings.TrimSpace(model), DefaultEffort: defaultEffort,
		SupportsReasoningEffort: preset.SupportsReasoningEffort,
	}
	if err := provider.Validate(); err != nil {
		return Provider{}, err
	}
	return provider, nil
}
