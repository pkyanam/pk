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
