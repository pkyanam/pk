package providers

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestPresetCatalogIsStableCompatibleAndSecretFree(t *testing.T) {
	presets := Presets()
	if len(presets) < 6 {
		t.Fatalf("catalog has only %d presets", len(presets))
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		if preset.ID == "" || seen[preset.ID] {
			t.Fatalf("empty or duplicate preset ID: %+v", preset)
		}
		seen[preset.ID] = true
		if preset.APIStyle == "openai_compatible" && preset.Protocol != ProtocolResponses && preset.Protocol != ProtocolChatCompletions && preset.Protocol != ProtocolCloudflareWorkersAI {
			t.Errorf("preset %s uses unhandled protocol %q", preset.ID, preset.Protocol)
		}
		if preset.ID == "anthropic" && (preset.APIStyle != "anthropic_messages" || preset.Protocol != ProtocolAnthropic || !strings.Contains(strings.ToLower(preset.CompatibilityNote), "not an openai-compatible")) {
			t.Errorf("Anthropic must be explicitly labeled as native Messages API: %+v", preset)
		}
		if preset.APIStyle == "" {
			t.Errorf("preset %s has no API style", preset.ID)
		}
		if !strings.HasPrefix(preset.BaseURL, "https://") || preset.APIKeyEnv == "" || preset.DocsURL == "" {
			t.Errorf("preset %s lacks an HTTPS endpoint, key variable, or docs URL: %+v", preset.ID, preset)
		}
		parsed, err := url.Parse(preset.BaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Errorf("preset %s has unsafe base URL %q", preset.ID, preset.BaseURL)
		}
	}
	if preset, ok := PresetByID("anthropic"); !ok || preset.APIStyle != "anthropic_messages" {
		t.Fatal("Anthropic must be listed explicitly as its native Messages API")
	}
	if preset, ok := PresetByID("cloudflare-workers-ai"); !ok || preset.Protocol != ProtocolCloudflareWorkersAI || !preset.RequiresAccountID {
		t.Fatal("Workers AI preset must be a separately labeled account-scoped direct endpoint")
	}
	presets[0].Label = "mutated"
	if Presets()[0].Label == "mutated" {
		t.Fatal("Presets() exposed mutable catalog backing storage")
	}
	encoded, err := json.Marshal(Presets())
	if err != nil || strings.Contains(string(encoded), "api_key\"") {
		t.Fatalf("preset catalog includes a secret field: %s, %v", encoded, err)
	}
}

func TestNewPresetProviderUsesLiveModelDiscoveryAndValidatesKey(t *testing.T) {
	provider, err := NewPresetProvider("groq", "", "secret-test-value", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != "groq" || provider.BaseURL != "https://api.groq.com/openai/v1" || provider.Protocol != ProtocolChatCompletions || provider.APIKey != "secret-test-value" || provider.DefaultModel != "" {
		t.Fatalf("preset provider = %+v", provider)
	}
	if err := provider.Validate(); err != nil {
		t.Fatalf("constructed provider invalid: %v", err)
	}
	for _, key := range []string{"", strings.Repeat("k", 4097), "bad\nheader"} {
		if _, err := NewPresetProvider("groq", "groq-test", key, "", ""); err == nil {
			t.Errorf("accepted invalid key of %d bytes", len(key))
		}
	}
	if _, err := NewPresetProvider("not-a-preset", "new", "key", "", ""); err == nil {
		t.Fatal("accepted unknown provider preset")
	}
	if _, err := NewPresetProvider("groq", "BAD-ID", "key", "", ""); err == nil {
		t.Fatal("accepted invalid provider ID")
	}
	form, err := NewPresetProvider("anthropic", "", "anthropic-test-key", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if form.Protocol != ProtocolAnthropic || form.SupportsReasoningEffort || form.DefaultEffort != "" {
		t.Fatalf("native Messages preset advertised generic effort: %+v", form)
	}
}
