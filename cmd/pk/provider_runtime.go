package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func resolveCLIProvider(choice string, useCodex bool) (*providers.Provider, error) {
	choice = strings.TrimSpace(choice)
	if useCodex {
		if choice != "" && choice != "native" && choice != "codex" {
			return nil, errors.New("--provider conflicts with --use-codex; choose one provider")
		}
		return nil, nil
	}
	if choice == "native" || choice == "codex" {
		return nil, nil
	}
	store := providers.Store{Home: pkHome()}
	id := choice
	if id == "" {
		var err error
		id, err = store.DefaultID()
		if err != nil {
			return nil, fmt.Errorf("load default provider: %w", err)
		}
	}
	if id == "" {
		return nil, nil
	}
	provider, err := store.Get(id)
	if err != nil {
		return nil, err
	}
	if _, err := provider.APIKeyValue(); err != nil {
		return nil, err
	}
	if err := store.ApplyModelPreference(&provider); err != nil {
		return nil, fmt.Errorf("load provider model preference: %w", err)
	}
	return &provider, nil
}

func prepareCLIAdapter(ctx context.Context, options *runner.Options, useCodex bool, codexPath string) (llm.Adapter, *providers.Provider, error) {
	if options == nil {
		return nil, nil, errors.New("runner options are required")
	}
	if options.ProviderID == "" && isCloudflareWorkersAIModel(options.Model) {
		return nil, nil, nativeCloudflareModelError()
	}
	if options.ProviderID != "" {
		provider, err := loadCLIProvider(options.ProviderID)
		if err != nil {
			return nil, nil, fmt.Errorf("load provider %q: %w", options.ProviderID, err)
		}
		client, err := providers.NewClient(*provider)
		if err != nil {
			return nil, nil, err
		}
		options.ProviderFingerprint = provider.Fingerprint()
		return client, provider, nil
	}
	options.ProviderFingerprint = ""
	client, err := prepareAdapter(ctx, useCodex, codexPath)
	return client, nil, err
}

func isCloudflareWorkersAIModel(model string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), "@cf/")
}

func nativeCloudflareModelError() error {
	return errors.New("model ID @cf/... requires the Cloudflare Workers AI provider; select that provider or set a native Codex model before sending")
}

// loadCLIProvider returns an in-memory credential snapshot for one run. The
// resolved API key is never written back to provider configuration or task state.
func loadCLIProvider(id string) (*providers.Provider, error) {
	provider, err := (providers.Store{Home: pkHome()}).Get(id)
	if err != nil {
		return nil, err
	}
	key, err := provider.APIKeyValue()
	if err != nil {
		return nil, err
	}
	provider.APIKey = key
	provider.APIKeyEnv = ""
	return &provider, nil
}

func applyProviderDefaults(options *runner.Options, provider *providers.Provider, modelSet, effortSet bool) {
	if options == nil || provider == nil {
		return
	}
	options.ProviderID = provider.ID
	if !modelSet && strings.TrimSpace(provider.DefaultModel) != "" {
		options.Model = provider.DefaultModel
	}
	if !effortSet && strings.TrimSpace(provider.DefaultEffort) != "" {
		options.Effort = strings.ToLower(strings.TrimSpace(provider.DefaultEffort))
	}
}
