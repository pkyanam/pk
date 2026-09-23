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
)

const storeVersion = 1
const maxStoreBytes = 2 << 20

type Store struct{ Home string }

type storeFile struct {
	Version   int        `json:"version"`
	Providers []Provider `json:"providers"`
	DefaultID string     `json:"default_id,omitempty"`
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
	err := store.withLock(func() error {
		file, err := store.load()
		if err != nil {
			return err
		}
		providers = append([]Provider(nil), file.Providers...)
		defaultID = file.DefaultID
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	result := make([]Summary, 0, len(providers))
	for _, provider := range providers {
		summary := Summarize(provider)
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
