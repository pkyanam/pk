package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
)

const providerBudgetCacheMaxAge = 7 * 24 * time.Hour

func resolveConfiguredContextBudget(providerID, modelID, providerBaseURL string, cfg config.ContextBudgetConfig) (contextbudget.Budget, error) {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return contextbudget.Budget{}, err
	}
	options := contextbudget.DefaultOptions()
	options.UnknownInputBudgetTokens = cloneTokenLimit(cfg.UnknownInputBudgetTokens)
	options.OutputReserveTokens = cloneTokenLimit(cfg.OutputReserveTokens)
	options.SafetyMarginTokens = cloneTokenLimit(cfg.SafetyMarginTokens)
	options.ProviderBaseURL = providerBaseURL
	overrides := make([]contextbudget.Override, 0, len(cfg.Overrides))
	for _, override := range cfg.Overrides {
		overrides = append(overrides, contextbudget.Override{
			ModelKey: contextbudget.ModelKey{ProviderID: override.ProviderID, ModelID: override.ModelID},
			Limits:   contextbudget.Limits{ContextTokens: cloneTokenLimit(override.ContextTokens), InputTokens: cloneTokenLimit(override.InputTokens), OutputTokens: cloneTokenLimit(override.OutputTokens)},
		})
	}
	reported, _, _ := cachedProviderModelLimits(providerID, modelID, providerBaseURL, time.Now())
	if providerID == "native" {
		if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
			limits, found, _ := contextbudget.ReadCodexModelCache(filepath.Join(codexHome, "models_cache.json"), modelID)
			if found && freshBudgetTimestamp(limits.FetchedAt, time.Now()) {
				reported = &limits
			}
		} else if userHome, homeErr := os.UserHomeDir(); homeErr == nil {
			limits, found, _ := contextbudget.ReadCodexModelCache(filepath.Join(userHome, ".codex", "models_cache.json"), modelID)
			if found && freshBudgetTimestamp(limits.FetchedAt, time.Now()) {
				reported = &limits
			}
		}
	}
	budget, err := contextbudget.Resolve(providerID, modelID, reported, overrides, options)
	if err != nil {
		return contextbudget.Budget{}, fmt.Errorf("resolve context budget: %w", err)
	}
	return budget, nil
}

func cachedProviderModelLimits(providerID, modelID, providerBaseURL string, now time.Time) (*contextbudget.Limits, bool, error) {
	if providerID == "" || providerID == "native" || providerID == "openai" && providerBaseURL == "" {
		return nil, false, nil
	}
	catalog, found, err := (providers.Store{Home: pkHome()}).LoadModelCatalog(providerID, providerBaseURL)
	if err != nil || !found {
		return nil, false, err
	}
	if !freshBudgetTimestamp(catalog.FetchedAt, now) {
		return nil, true, nil
	}
	for _, model := range catalog.Models {
		if model.ID == modelID && (model.ContextTokens != nil || model.InputTokens != nil || model.OutputTokens != nil) {
			return &contextbudget.Limits{ContextTokens: cloneTokenLimit(model.ContextTokens), InputTokens: cloneTokenLimit(model.InputTokens), OutputTokens: cloneTokenLimit(model.OutputTokens), Source: contextbudget.SourceProviderCache}, true, nil
		}
	}
	return nil, true, nil
}

func freshBudgetTimestamp(fetchedAt, now time.Time) bool {
	return !fetchedAt.IsZero() && !now.Before(fetchedAt) && now.Sub(fetchedAt) <= providerBudgetCacheMaxAge
}

func contextBudgetCatalogState(providerID, modelID, providerBaseURL string, now time.Time) (found, stale bool, fetchedAt time.Time) {
	if providerID == "" || providerID == "native" {
		path := ""
		if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
			path = filepath.Join(codexHome, "models_cache.json")
		} else if userHome, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(userHome, ".codex", "models_cache.json")
		}
		if path == "" {
			return false, false, time.Time{}
		}
		limits, ok, err := contextbudget.ReadCodexModelCache(path, modelID)
		if err != nil || !ok {
			return false, false, time.Time{}
		}
		return true, !freshBudgetTimestamp(limits.FetchedAt, now), limits.FetchedAt
	}
	catalog, ok, err := (providers.Store{Home: pkHome()}).LoadModelCatalog(providerID, providerBaseURL)
	if err != nil || !ok {
		return false, false, time.Time{}
	}
	for _, model := range catalog.Models {
		if model.ID == modelID {
			return true, !freshBudgetTimestamp(catalog.FetchedAt, now), catalog.FetchedAt
		}
	}
	return false, !freshBudgetTimestamp(catalog.FetchedAt, now), catalog.FetchedAt
}

func cloneTokenLimit(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func configuredHistoryCompaction(cfg config.HistoryCompactionConfig) runner.HistoryCompactionOptions {
	cfg = cfg.Normalized()
	return runner.HistoryCompactionOptions{
		Enabled: *cfg.Enabled, TriggerRatio: *cfg.TriggerRatio, TargetRatio: *cfg.TargetRatio,
		SummaryReserveTokens: *cfg.SummaryReserveTokens, SummaryInputTokens: *cfg.SummaryInputTokens,
		MaxSummaryTokens: *cfg.MaxSummaryTokens, MaxSummaryCalls: int(*cfg.MaxSummaryCalls),
	}
}

func applyConfiguredContextManagement(options *runner.Options, cfg config.Config, providerBaseURL string) error {
	if options == nil {
		return fmt.Errorf("runner options are required")
	}
	providerID := strings.TrimSpace(options.ProviderID)
	if providerID == "" {
		providerID = "native"
	}
	budget, err := resolveConfiguredContextBudget(providerID, options.Model, providerBaseURL, cfg.ContextBudget)
	if err != nil {
		return err
	}
	options.ContextBudget = budget
	options.HistoryCompaction = configuredHistoryCompaction(cfg.HistoryCompaction)
	return nil
}
