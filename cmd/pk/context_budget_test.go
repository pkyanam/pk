package main

import (
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/providers"
)

func TestResolveConfiguredContextBudgetUsesFreshProviderCacheThenExpires(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	contextLimit, inputLimit, outputLimit := int64(200000), int64(180000), int64(32000)
	store := providers.Store{Home: home}
	if err := store.SaveModelCatalog("example", "https://example.test/v1", []providers.Model{{ID: "model-x", ContextTokens: &contextLimit, InputTokens: &inputLimit, OutputTokens: &outputLimit}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	budget, err := resolveConfiguredContextBudget("example", "model-x", "https://example.test/v1", config.DefaultContextBudgetConfig())
	if err != nil {
		t.Fatal(err)
	}
	if budget.ContextTokens == nil || *budget.ContextTokens != contextLimit || budget.ContextSource != "provider_cache" || budget.InputSource != "provider_cache" || budget.OperationalInputBudgetTokens != 170904 {
		t.Fatalf("fresh provider limits not applied: %+v", budget)
	}
	if err := store.SaveModelCatalog("example", "https://example.test/v1", []providers.Model{{ID: "model-x", ContextTokens: &contextLimit, InputTokens: &inputLimit, OutputTokens: &outputLimit}}, time.Now().Add(-providerBudgetCacheMaxAge-time.Hour)); err != nil {
		t.Fatal(err)
	}
	stale, err := resolveConfiguredContextBudget("example", "model-x", "https://example.test/v1", config.DefaultContextBudgetConfig())
	if err != nil {
		t.Fatal(err)
	}
	if stale.ContextTokens != nil || stale.ContextSource != "unknown" || stale.OperationalInputBudgetTokens != 128000 {
		t.Fatalf("stale provider limits affected budget: %+v", stale)
	}
}

func TestResolveConfiguredCloudflareGLMContextUsesFallbackThenProviderCacheAndOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PK_HOME", home)
	providerID := "cloudflare-workers-ai"
	modelID := "@cf/zai-org/glm-5.3-flash"
	baseURL := "https://api.cloudflare.com/client/v4/accounts/0123456789abcdef0123456789abcdef/ai/v1"
	cfg := config.DefaultContextBudgetConfig()

	fallback, err := resolveConfiguredContextBudget(providerID, modelID, baseURL, cfg)
	if err != nil || fallback.ContextTokens == nil || *fallback.ContextTokens != 1_310_720 || fallback.ContextSource != "official_catalog" {
		t.Fatalf("official model fallback = %+v err=%v", fallback, err)
	}

	providerContext := int64(1_250_000)
	if err := (providers.Store{Home: home}).SaveModelCatalog(providerID, baseURL, []providers.Model{{ID: modelID, ContextTokens: &providerContext}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cached, err := resolveConfiguredContextBudget(providerID, modelID, baseURL, cfg)
	if err != nil || cached.ContextTokens == nil || *cached.ContextTokens != providerContext || cached.ContextSource != "provider_cache" {
		t.Fatalf("provider model metadata did not take precedence: %+v err=%v", cached, err)
	}

	userContext := int64(900_000)
	cfg.Overrides = []config.ContextBudgetOverride{{ProviderID: providerID, ModelID: modelID, ContextTokens: &userContext}}
	overridden, err := resolveConfiguredContextBudget(providerID, modelID, baseURL, cfg)
	if err != nil || overridden.ContextTokens == nil || *overridden.ContextTokens != userContext || overridden.ContextSource != "user_override" {
		t.Fatalf("explicit limit override did not take precedence: %+v err=%v", overridden, err)
	}
}
