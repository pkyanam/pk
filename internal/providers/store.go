package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkyanam/pk/internal/config"
)

const storeVersion = 1
const maxStoreBytes = 2 << 20

type Store struct{ Home string }

type storeFile struct {
	Version   int        `json:"version"`
	Providers []Provider `json:"providers"`
	DefaultID string     `json:"default_id,omitempty"`
}

type modelPreferencesFile struct {
	Version int                        `json:"version"`
	Items   map[string]ModelPreference `json:"items"`
}

// ModelPreference is a per-provider user selection, separate from connection
// identity so changing the preferred model does not invalidate saved sessions.
type ModelPreference struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

func (store Store) SetDefaultModelAndProvider(id, model string, effort *string) error {
	if !providerIDPattern.MatchString(id) {
		return fmt.Errorf("invalid provider ID %q", id)
	}
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 256 || strings.ContainsAny(model, "\r\n\x00") {
		return errors.New("provider model ID must be non-empty, at most 256 bytes, and contain no control characters")
	}
	return store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		found := false
		var selected Provider
		for _, provider := range file.Providers {
			if provider.ID == id {
				found = true
				selected = provider
				break
			}
		}
		if !found {
			return fmt.Errorf("provider %q is not configured", id)
		}
		preferences, err := store.loadModelPreferences()
		if err != nil {
			return err
		}
		preference := preferences[id]
		preference.Model = model
		if effort != nil && selected.SupportsReasoningEffort {
			value := strings.ToLower(strings.TrimSpace(*effort))
			if value != "" && !config.ValidEffort(value) {
				return fmt.Errorf("provider %q has an unsupported preferred effort", id)
			}
			preference.Effort = value
		}
		if preferences == nil {
			preferences = make(map[string]ModelPreference)
		}
		preferences[id] = preference
		if len(preferences) > 256 {
			return errors.New("provider model preferences are limited to 256 entries")
		}
		if err := store.saveModelPreferences(preferences); err != nil {
			return err
		}
		file.DefaultID = id
		return store.save(file)
	})
}

func (store Store) ModelPreference(id string) (ModelPreference, bool, error) {
	var preference ModelPreference
	var found bool
	err := store.withLock(func() error {
		if _, err := store.load(); err != nil {
			return err
		}
		preferences, err := store.loadModelPreferences()
		if err != nil {
			return err
		}
		preference, found = preferences[id]
		return nil
	})
	return preference, found, err
}

func (store Store) ApplyModelPreference(provider *Provider) error {
	if provider == nil {
		return nil
	}
	preference, found, err := store.ModelPreference(provider.ID)
	if err != nil || !found {
		return err
	}
	if preference.Model != "" {
		provider.DefaultModel = preference.Model
	}
	if preference.Effort != "" {
		provider.DefaultEffort = preference.Effort
	}
	return nil
}

// DefaultID returns the configured default provider ID, or an empty string
// when native Codex is the default.
func (store Store) DefaultID() (string, error) {
	var result string
	err := store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		result = file.DefaultID
		return nil
	})
	return result, err
}

// SetDefault selects a configured provider. An empty ID restores native Codex.
func (store Store) SetDefault(id string) error {
	if id != "" && !providerIDPattern.MatchString(id) {
		return fmt.Errorf("invalid provider ID %q", id)
	}
	return store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		if id != "" {
			found := false
			for _, provider := range file.Providers {
				if provider.ID == id {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("provider %q is not configured", id)
			}
		}
		file.DefaultID = id
		return store.save(file)
	})
}

func (store Store) List() ([]Provider, error) {
	var result []Provider
	err := store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		result = append([]Provider(nil), file.Providers...)
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, err
}

func (store Store) Get(id string) (Provider, error) {
	var result Provider
	err := store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		for _, provider := range file.Providers {
			if provider.ID == id {
				result = provider
				return nil
			}
		}
		return fmt.Errorf("provider %q is not configured", id)
	})
	return result, err
}

func (store Store) Put(provider Provider) error {
	base, err := NormalizeBaseURL(provider.BaseURL)
	if err != nil {
		return err
	}
	provider.BaseURL = base
	if provider.DefaultEffort != "" {
		provider.DefaultEffort = strings.ToLower(strings.TrimSpace(provider.DefaultEffort))
	}
	if err := provider.Validate(); err != nil {
		return err
	}
	return store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		found := false
		for index := range file.Providers {
			if file.Providers[index].ID == provider.ID {
				file.Providers[index] = provider
				found = true
				break
			}
		}
		if !found {
			file.Providers = append(file.Providers, provider)
		}
		sort.Slice(file.Providers, func(i, j int) bool { return file.Providers[i].ID < file.Providers[j].ID })
		return store.save(file)
	})
}

func (store Store) Remove(id string) error {
	if !providerIDPattern.MatchString(id) {
		return fmt.Errorf("invalid provider ID %q", id)
	}
	return store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		for index := range file.Providers {
			if file.Providers[index].ID == id {
				file.Providers = append(file.Providers[:index], file.Providers[index+1:]...)
				preferences, err := store.loadModelPreferences()
				if err != nil {
					return err
				}
				delete(preferences, id)
				if err := store.saveModelPreferences(preferences); err != nil {
					return err
				}
				if file.DefaultID == id {
					file.DefaultID = ""
				}
				return store.save(file)
			}
		}
		return fmt.Errorf("provider %q is not configured", id)
	})
}

func (store Store) Summaries() ([]Summary, error) {
	var providers []Provider
	var defaultID string
	var preferences map[string]ModelPreference
	err := store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		providers = append([]Provider(nil), file.Providers...)
		defaultID = file.DefaultID
		preferences, err = store.loadModelPreferences()
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	result := make([]Summary, 0, len(providers))
	for _, provider := range providers {
		summary := Summarize(provider)
		if preference, ok := preferences[provider.ID]; ok {
			if preference.Model != "" {
				summary.DefaultModel = preference.Model
			}
			if preference.Effort != "" {
				summary.DefaultEffort = preference.Effort
			}
		}
		summary.IsDefault = provider.ID == defaultID
		result = append(result, summary)
	}
	return result, nil
}

func (store Store) load() (storeFile, error) {
	path := store.configPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return storeFile{Version: storeVersion, Providers: []Provider{}}, nil
	}
	if err != nil {
		return storeFile{}, fmt.Errorf("inspect provider config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return storeFile{}, errors.New("provider config must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return storeFile{}, errors.New("provider config permissions must be 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return storeFile{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
	if err != nil {
		return storeFile{}, err
	}
	if len(data) > maxStoreBytes {
		return storeFile{}, errors.New("provider config exceeds 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result storeFile
	if err := decoder.Decode(&result); err != nil {
		return storeFile{}, fmt.Errorf("parse provider config: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return storeFile{}, errors.New("provider config must contain one JSON object")
	}
	if result.Version != storeVersion {
		return storeFile{}, fmt.Errorf("unsupported provider config version %d", result.Version)
	}
	seen := make(map[string]bool)
	for _, provider := range result.Providers {
		if err := provider.Validate(); err != nil || seen[provider.ID] {
			return storeFile{}, errors.New("provider config contains invalid or duplicate providers")
		}
		seen[provider.ID] = true
	}
	if result.DefaultID != "" && !seen[result.DefaultID] {
		return storeFile{}, errors.New("provider config default references an unknown provider")
	}
	sort.Slice(result.Providers, func(i, j int) bool { return result.Providers[i].ID < result.Providers[j].ID })
	return result, nil
}

func (store Store) loadModelPreferences() (map[string]ModelPreference, error) {
	path := store.modelPreferencesPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]ModelPreference{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect provider model preferences: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("provider model preferences must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStoreBytes {
		return nil, errors.New("provider model preferences exceed 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result modelPreferencesFile
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("parse provider model preferences: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || result.Version != 1 || len(result.Items) > 256 {
		return nil, errors.New("provider model preferences are invalid")
	}
	for id, preference := range result.Items {
		if !providerIDPattern.MatchString(id) || preference.Model == "" || len(preference.Model) > 256 || strings.ContainsAny(preference.Model, "\r\n\x00") || (preference.Effort != "" && !config.ValidEffort(preference.Effort)) {
			return nil, errors.New("provider model preferences contain an invalid entry")
		}
	}
	if result.Items == nil {
		result.Items = make(map[string]ModelPreference)
	}
	return result.Items, nil
}

func (store Store) saveModelPreferences(items map[string]ModelPreference) error {
	if len(items) == 0 {
		if err := os.Remove(store.modelPreferencesPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	home, err := store.ensureHome()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(modelPreferencesFile{Version: 1, Items: items}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".provider-preferences-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, store.modelPreferencesPath()); err != nil {
		return err
	}
	directory, err := os.Open(home)
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	return err
}

func (store Store) save(file storeFile) error {
	home, err := store.ensureHome()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(storeFile{Version: storeVersion, Providers: file.Providers, DefaultID: file.DefaultID}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".providers-*.tmp")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(path, store.configPath()); err != nil {
		return err
	}
	directory, err := os.Open(home)
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	return err
}

func (store Store) ensureHome() (string, error) {
	if strings.TrimSpace(store.Home) == "" {
		return "", errors.New("provider config directory is required")
	}
	home, err := filepath.Abs(store.Home)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("provider config directory must be a real directory")
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return "", err
	}
	return home, nil
}

func (store Store) withLock(action func() error) error {
	home, err := store.ensureHome()
	if err != nil {
		return err
	}
	unlock, err := lockProviderConfig(filepath.Join(home, "providers.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return action()
}

func (store Store) configPath() string {
	home, err := filepath.Abs(store.Home)
	if err != nil {
		home = store.Home
	}
	return filepath.Join(home, "providers.json")
}

func (store Store) modelPreferencesPath() string {
	home, err := filepath.Abs(store.Home)
	if err != nil {
		home = store.Home
	}
	return filepath.Join(home, "provider-preferences.json")
}
