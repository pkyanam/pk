package contextbudget

import "testing"

func TestCloudflareGLM53FlashOfficialContextFallbackAndPrecedence(t *testing.T) {
	modelID := "@cf/zai-org/glm-5.3-flash"
	baseURL := "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1"
	options := DefaultOptions()
	options.ProviderBaseURL = baseURL

	fallback, err := Resolve("cloudflare-workers-ai", modelID, nil, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.ContextTokens == nil || *fallback.ContextTokens != 1_310_720 || fallback.ContextSource != SourceOfficialCatalog {
		t.Fatalf("official Cloudflare context fallback = %+v", fallback)
	}
	if fallback.InputTokens != nil || fallback.InputSource != SourceUnknown {
		t.Fatalf("fallback invented an input limit: %+v", fallback)
	}

	apiContext := int64(1_200_000)
	reported, err := Resolve("cloudflare-workers-ai", modelID, &Limits{ContextTokens: &apiContext, Source: SourceProviderReported}, nil, options)
	if err != nil || reported.ContextTokens == nil || *reported.ContextTokens != apiContext || reported.ContextSource != SourceProviderReported {
		t.Fatalf("provider report did not override official fallback: budget=%+v err=%v", reported, err)
	}

	userContext := int64(900_000)
	overridden, err := Resolve("cloudflare-workers-ai", modelID, &Limits{ContextTokens: &apiContext, Source: SourceProviderReported}, []Override{{ModelKey: ModelKey{ProviderID: "cloudflare-workers-ai", ModelID: modelID}, Limits: Limits{ContextTokens: &userContext}}}, options)
	if err != nil || overridden.ContextTokens == nil || *overridden.ContextTokens != userContext || overridden.ContextSource != SourceUserOverride {
		t.Fatalf("user override did not take precedence: budget=%+v err=%v", overridden, err)
	}
}

func TestCloudflareOfficialContextFallbackRequiresExactModelAndEndpoint(t *testing.T) {
	options := DefaultOptions()
	for _, test := range []struct {
		provider string
		model    string
		baseURL  string
	}{
		{"cloudflare-workers-ai", "@cf/zai-org/glm-5.3-flash-preview", "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1"},
		{"cloudflare-workers-ai", "@cf/zai-org/glm-5.3-flash", "https://api.example.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1"},
		{"cloudflare-workers-ai", "@cf/zai-org/glm-5.3-flash", "https://api.cloudflare.com/client/v4/accounts/invalid/ai/v1"},
	} {
		options.ProviderBaseURL = test.baseURL
		budget, err := Resolve(test.provider, test.model, nil, nil, options)
		if err != nil {
			t.Fatal(err)
		}
		if budget.ContextTokens != nil || budget.ContextSource != SourceUnknown {
			t.Errorf("unexpected fallback for provider=%q model=%q url=%q: %+v", test.provider, test.model, test.baseURL, budget)
		}
	}
}
