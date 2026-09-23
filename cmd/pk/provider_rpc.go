package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/pkyanam/pk/internal/providers"
)

type providersUpdatedPayload struct {
	Providers         []providers.Summary `json:"providers"`
	DefaultProviderID string              `json:"default_provider_id"`
	NextSessionOnly   bool                `json:"next_session_only"`
	AddedProviderID   string              `json:"added_provider_id,omitempty"`
	PresetID          string              `json:"preset_id,omitempty"`
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

func rpcProviderPresets() []providers.Preset { return providers.Presets() }

func putRPCProviderPreset(store providers.Store, presetID, id, key string, model, effort *string) (providers.Provider, error) {
	preset, ok := providers.PresetByID(presetID)
	if !ok {
		return providers.Provider{}, fmt.Errorf("unknown provider preset")
	}
	id = strings.TrimSpace(id)
	provider, err := providers.NewPresetProvider(presetID, id, key, "", "")
	if err != nil {
		return providers.Provider{}, err
	}
	existingProviders, err := store.List()
	if err != nil {
		return providers.Provider{}, fmt.Errorf("read provider configuration")
	}
	for _, existing := range existingProviders {
		if existing.ID != provider.ID {
			continue
		}
		existingBase, _ := providers.NormalizeBaseURL(existing.BaseURL)
		if existing.Protocol != provider.Protocol || existingBase != preset.BaseURL {
			return providers.Provider{}, fmt.Errorf("provider ID already belongs to a different endpoint")
		}
		// Reconnecting with a key alone must not reset the user's model or effort.
		provider.DefaultModel = existing.DefaultModel
		provider.DefaultEffort = existing.DefaultEffort
		break
	}
	if model != nil {
		provider.DefaultModel = strings.TrimSpace(*model)
	}
	if effort != nil {
		provider.DefaultEffort = strings.TrimSpace(*effort)
	}
	if err := store.Put(provider); err != nil {
		return providers.Provider{}, fmt.Errorf("save provider configuration")
	}
	return provider, nil
}

type providerPresetAddRequest struct {
	PresetID      string  `json:"preset_id"`
	ID            string  `json:"id,omitempty"`
	APIKey        string  `json:"api_key"`
	DefaultModel  *string `json:"default_model,omitempty"`
	DefaultEffort *string `json:"default_effort,omitempty"`
}

func decodeProviderPresetAdd(data []byte) (providerPresetAddRequest, error) {
	var request providerPresetAddRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return providerPresetAddRequest{}, fmt.Errorf("invalid preset configuration")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return providerPresetAddRequest{}, fmt.Errorf("invalid preset configuration")
	}
	return request, nil
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
