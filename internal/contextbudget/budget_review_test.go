package contextbudget

import (
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestResolveUsesPerFieldSourcesAndExactProviderModelKey(t *testing.T) {
	reported := &Limits{
		ContextTokens: int64ptr(100_000),
		InputTokens:   int64ptr(80_000),
		OutputTokens:  int64ptr(4_000),
	}
	overrides := []Override{
		{ModelKey: ModelKey{ProviderID: "other", ModelID: "model-x"}, Limits: Limits{InputTokens: int64ptr(1)}},
		{ModelKey: ModelKey{ProviderID: "test", ModelID: "model-x"}, Limits: Limits{InputTokens: int64ptr(70_000), OutputTokens: int64ptr(5_000)}},
	}
	options := Options{UnknownInputBudgetTokens: int64ptr(90_000), OutputReserveTokens: int64ptr(10_000), SafetyMarginTokens: int64ptr(1_000)}

	got, err := Resolve(" test ", " model-x ", reported, overrides, options)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextTokens == nil || *got.ContextTokens != 100_000 || got.ContextSource != SourceProviderReported {
		t.Fatalf("context limit/source = %v/%q", got.ContextTokens, got.ContextSource)
	}
	if got.InputTokens == nil || *got.InputTokens != 70_000 || got.InputSource != SourceUserOverride {
		t.Fatalf("input limit/source = %v/%q", got.InputTokens, got.InputSource)
	}
	if got.OutputTokens == nil || *got.OutputTokens != 5_000 || got.OutputSource != SourceUserOverride {
		t.Fatalf("output limit/source = %v/%q", got.OutputTokens, got.OutputSource)
	}
	if got.OutputReserveTokens != 5_000 {
		t.Fatalf("effective output reserve = %d, want clamp to known output limit 5000", got.OutputReserveTokens)
	}
	if got.OperationalInputBudgetTokens != 69_000 || got.OperationalInputSource != SourceDerived {
		t.Fatalf("derived operational budget = %d/%q, want 69000/derived", got.OperationalInputBudgetTokens, got.OperationalInputSource)
	}
}

func TestExplicitZeroOutputReserveIsPreserved(t *testing.T) {
	got, err := Resolve("custom", "small-model", &Limits{ContextTokens: int64ptr(1_000)}, nil, Options{
		UnknownInputBudgetTokens: int64ptr(64_000),
		OutputReserveTokens:      int64ptr(0),
		SafetyMarginTokens:       int64ptr(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.OutputReserveTokens != 0 || got.OperationalInputBudgetTokens != 1_000 {
		t.Fatalf("explicit zero reserve was defaulted: reserve=%d budget=%d", got.OutputReserveTokens, got.OperationalInputBudgetTokens)
	}
}

func TestUnknownBudgetIsOperationalAllowanceNotPublishedCapacity(t *testing.T) {
	options := Options{UnknownInputBudgetTokens: int64ptr(81_000), OutputReserveTokens: int64ptr(20_000), SafetyMarginTokens: int64ptr(4_000)}
	got, err := Resolve("custom", "opaque-model", nil, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasPublishedLimit() || got.ContextTokens != nil || got.InputTokens != nil || got.OutputTokens != nil {
		t.Fatalf("unknown model exposed a capacity: %+v", got)
	}
	if got.OperationalInputBudgetTokens != 81_000 || got.OperationalInputSource != SourceOperational {
		t.Fatalf("operational fallback = %d/%q", got.OperationalInputBudgetTokens, got.OperationalInputSource)
	}
}

func TestOfficialCatalogLimitsRequireOfficialProviderOrigin(t *testing.T) {
	withoutOrigin, err := Resolve("openai", "gpt-6-luna", nil, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if withoutOrigin.HasPublishedLimit() {
		t.Fatalf("provider ID alone asserted a catalog capacity: %+v", withoutOrigin)
	}
	withOrigin, err := Resolve("openai", "gpt-6-luna", nil, nil, Options{
		ProviderBaseURL: "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !withOrigin.HasPublishedLimit() || withOrigin.ContextSource != SourceOfficialCatalog {
		t.Fatalf("official endpoint did not resolve its catalog entry: %+v", withOrigin)
	}
}

func TestModelSwitchDoesNotReusePreviousModelOverride(t *testing.T) {
	overrides := []Override{{
		ModelKey: ModelKey{ProviderID: "custom", ModelID: "luna"},
		Limits:   Limits{ContextTokens: int64ptr(96_000), OutputTokens: int64ptr(8_000)},
	}}
	options := Options{UnknownInputBudgetTokens: int64ptr(120_000), OutputReserveTokens: int64ptr(10_000), SafetyMarginTokens: int64ptr(2_000)}

	luna, err := Resolve("custom", "luna", nil, overrides, options)
	if err != nil {
		t.Fatal(err)
	}
	sol, err := Resolve("custom", "sol", nil, overrides, options)
	if err != nil {
		t.Fatal(err)
	}
	if luna.OperationalInputBudgetTokens != 86_000 || luna.OperationalInputSource != SourceDerived {
		t.Fatalf("Luna budget = %d/%q", luna.OperationalInputBudgetTokens, luna.OperationalInputSource)
	}
	if sol.HasPublishedLimit() || sol.OperationalInputBudgetTokens != 120_000 || sol.OperationalInputSource != SourceOperational {
		t.Fatalf("Sol inherited Luna metadata: %+v", sol)
	}
}

func TestResolvePreservesCachedProviderLimitProvenance(t *testing.T) {
	fetched := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	got, err := Resolve("router", "model", &Limits{
		ContextTokens: int64ptr(200_000), InputTokens: int64ptr(100_000),
		Source: SourceProviderCache, FetchedAt: fetched,
	}, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextSource != SourceProviderCache || got.InputSource != SourceProviderCache || got.LimitsFetchedAt == nil || !got.LimitsFetchedAt.Equal(fetched) {
		t.Fatalf("cached metadata provenance was lost: %+v", got)
	}
}

func TestEstimateRequestUsesBoundedLabeledMultimodalAllowance(t *testing.T) {
	item := llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "c", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultImage, Value: "image-ref"}}}}
	got := EstimateRequest(llm.Request{Input: []llm.Item{item}})
	if got.Tokens == nil || *got.Tokens < EstimatedTokensPerImage || got.Confidence != "very_low" || got.UnknownComponentBytes != int64(len("image-ref")) {
		t.Fatalf("multimodal estimate did not disclose its allowance/uncertainty: %+v", got)
	}
	if got.Reason == "" {
		t.Fatalf("multimodal estimate omitted its limitation: %+v", got)
	}
}

func TestEstimateRequestMarksOpaqueReasoningEstimateVeryLowConfidence(t *testing.T) {
	payload := `{"opaque":"payload"}`
	item := llm.Item{Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: []byte(payload)}}
	got := EstimateRequest(llm.Request{Input: []llm.Item{item}})
	if got.Tokens == nil || got.Confidence != "very_low" || got.UnknownComponentBytes != int64(len(payload)) {
		t.Fatalf("opaque reasoning was not distinguished from ordinary text: %+v", got)
	}
}
