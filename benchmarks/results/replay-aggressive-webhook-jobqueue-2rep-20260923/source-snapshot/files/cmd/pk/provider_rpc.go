package main

import (
	"context"
	"fmt"

	"github.com/pkyanam/pk/internal/providers"
)

type providersUpdatedPayload struct {
	Providers         []providers.Summary `json:"providers"`
	DefaultProviderID string              `json:"default_provider_id"`
	NextSessionOnly   bool                `json:"next_session_only"`
}

func rpcProviderStore() providers.Store { return providers.Store{Home: pkHome()} }

func resolveRPCProvider(id string) (providers.Provider, error) {
	if id == "" {
		return providers.Provider{}, nil
	}
	provider, err := rpcProviderStore().Get(id)
	if err != nil {
		return providers.Provider{}, err
	}
	if _, err := provider.APIKeyValue(); err != nil {
		return providers.Provider{}, err
	}
	return provider, nil
}

func resolveRPCProviderID(id string) (string, error) {
	if id != "" {
		if _, err := resolveRPCProvider(id); err != nil {
			return "", err
		}
		return id, nil
	}
	defaultID, err := rpcProviderStore().DefaultID()
	if err != nil || defaultID == "" {
		return "", err
	}
	if _, err := resolveRPCProvider(defaultID); err != nil {
		return "", fmt.Errorf("configured default provider %q is unavailable: %w", defaultID, err)
	}
	return defaultID, nil
}

func rpcProviderModels(ctx context.Context, id string) ([]providers.Model, error) {
	provider, err := resolveRPCProvider(id)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("provider_id is required")
	}
	return provider.Models(ctx)
}

func rpcProvidersUpdated() (providersUpdatedPayload, error) {
	store := rpcProviderStore()
	items, err := store.Summaries()
	if err != nil {
		return providersUpdatedPayload{}, err
	}
	defaultID, err := store.DefaultID()
	if err != nil {
		return providersUpdatedPayload{}, err
	}
	return providersUpdatedPayload{Providers: items, DefaultProviderID: defaultID, NextSessionOnly: true}, nil
}

func (s *rpcServer) providerMutationAllowed(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active || s.releaseActive || s.attachedTask != "" || s.taskFollowCancel != nil {
		return fmt.Errorf("provider configuration cannot change while a turn or task is active")
	}
	if providerID != "" && s.session != "" && s.providerID == providerID {
		return fmt.Errorf("provider %q is pinned by the current session; start a new session before changing it", providerID)
	}
	return nil
}
