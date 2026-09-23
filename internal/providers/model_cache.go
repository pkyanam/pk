package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	modelCacheVersion  = 1
	maxModelCacheBytes = 4 << 20
	maxCachedModels    = 20_000
)

var cachedModelIDPattern = regexp.MustCompile(`^[^\r\n\x00]{1,256}$`)

type ModelCatalog struct {
	ProviderID string    `json:"provider_id"`
	BaseURL    string    `json:"base_url"`
	FetchedAt  time.Time `json:"fetched_at"`
	Models     []Model   `json:"models"`
}

type modelCacheFile struct {
	Version  int            `json:"version"`
	Catalogs []ModelCatalog `json:"catalogs"`
}

// SaveModelCatalog stores only public model metadata returned by a provider.
// Credentials and provider settings are stored in a different file.
func (store Store) SaveModelCatalog(providerID, baseURL string, models []Model, fetchedAt time.Time) error {
	providerID = strings.TrimSpace(providerID)
	if !providerIDPattern.MatchString(providerID) || len(models) > maxCachedModels {
		return errors.New("invalid provider model catalog")
	}
	baseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return fmt.Errorf("provider model catalog base URL: %w", err)
	}
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	fetchedAt = fetchedAt.UTC()
	clean := make([]Model, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if !cachedModelIDPattern.MatchString(model.ID) {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		if !validOptionalLimit(model.ContextTokens) || !validOptionalLimit(model.InputTokens) || !validOptionalLimit(model.OutputTokens) {
			return fmt.Errorf("model %q has an invalid token limit", model.ID)
		}
		seen[model.ID] = struct{}{}
		model.LimitsSource = "provider_reported"
		clean = append(clean, model)
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].ID < clean[j].ID })
	return store.withLock(func() error {
		file, err := store.loadModelCache()
		if err != nil {
			return err
		}
		found := false
		for i := range file.Catalogs {
			if file.Catalogs[i].ProviderID == providerID {
				file.Catalogs[i] = ModelCatalog{ProviderID: providerID, BaseURL: baseURL, FetchedAt: fetchedAt, Models: clean}
				found = true
				break
			}
		}
		if !found {
			file.Catalogs = append(file.Catalogs, ModelCatalog{ProviderID: providerID, BaseURL: baseURL, FetchedAt: fetchedAt, Models: clean})
		}
		total := 0
		for _, catalog := range file.Catalogs {
			total += len(catalog.Models)
		}
		if total > maxCachedModels {
			return errors.New("provider model cache contains too many models")
		}
		sort.Slice(file.Catalogs, func(i, j int) bool { return file.Catalogs[i].ProviderID < file.Catalogs[j].ProviderID })
		return store.saveModelCache(file)
	})
}

// LoadModelCatalog returns a provider's last fetched public model metadata.
// Callers choose and disclose their own freshness policy using FetchedAt.
func (store Store) LoadModelCatalog(providerID, baseURL string) (ModelCatalog, bool, error) {
	providerID = strings.TrimSpace(providerID)
	if !providerIDPattern.MatchString(providerID) {
		return ModelCatalog{}, false, errors.New("invalid provider ID")
	}
	baseURL, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return ModelCatalog{}, false, fmt.Errorf("provider model catalog base URL: %w", err)
	}
	var result ModelCatalog
	err = store.withLock(func() error {
		file, err := store.loadModelCache()
		if err != nil {
			return err
		}
		for _, catalog := range file.Catalogs {
			if catalog.ProviderID == providerID && catalog.BaseURL == baseURL {
				result = catalog
				result.Models = append([]Model(nil), catalog.Models...)
				for i := range result.Models {
					result.Models[i].LimitsSource = "provider_cache"
				}
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return ModelCatalog{}, false, err
	}
	return result, result.ProviderID != "", nil
}

func (store Store) loadModelCache() (modelCacheFile, error) {
	path := store.modelCachePath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return modelCacheFile{Version: modelCacheVersion, Catalogs: []ModelCatalog{}}, nil
	}
	if err != nil {
		return modelCacheFile{}, fmt.Errorf("inspect provider model cache: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return modelCacheFile{}, errors.New("provider model cache must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return modelCacheFile{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxModelCacheBytes+1))
	if err != nil {
		return modelCacheFile{}, err
	}
	if len(data) > maxModelCacheBytes {
		return modelCacheFile{}, errors.New("provider model cache exceeds 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result modelCacheFile
	if err := decoder.Decode(&result); err != nil {
		return modelCacheFile{}, errors.New("provider model cache is invalid")
	}
	if decoder.Decode(new(any)) != io.EOF || result.Version != modelCacheVersion || len(result.Catalogs) > 64 {
		return modelCacheFile{}, errors.New("provider model cache has an unsupported shape")
	}
	seenProviders := make(map[string]struct{}, len(result.Catalogs))
	totalModels := 0
	for i := range result.Catalogs {
		catalog := &result.Catalogs[i]
		if !providerIDPattern.MatchString(catalog.ProviderID) || catalog.FetchedAt.IsZero() || len(catalog.Models) > maxCachedModels {
			return modelCacheFile{}, errors.New("provider model cache has invalid catalog metadata")
		}
		if _, exists := seenProviders[catalog.ProviderID]; exists {
			return modelCacheFile{}, errors.New("provider model cache contains duplicate catalogs")
		}
		seenProviders[catalog.ProviderID] = struct{}{}
		base, err := NormalizeBaseURL(catalog.BaseURL)
		if err != nil || base != catalog.BaseURL {
			return modelCacheFile{}, errors.New("provider model cache contains an invalid base URL")
		}
		seenModels := make(map[string]struct{}, len(catalog.Models))
		for _, model := range catalog.Models {
			if !cachedModelIDPattern.MatchString(model.ID) || !validOptionalLimit(model.ContextTokens) || !validOptionalLimit(model.InputTokens) || !validOptionalLimit(model.OutputTokens) {
				return modelCacheFile{}, errors.New("provider model cache contains invalid model metadata")
			}
			if _, exists := seenModels[model.ID]; exists {
				return modelCacheFile{}, errors.New("provider model cache contains duplicate model IDs")
			}
			seenModels[model.ID] = struct{}{}
		}
		totalModels += len(catalog.Models)
		if totalModels > maxCachedModels {
			return modelCacheFile{}, errors.New("provider model cache contains too many models")
		}
	}
	return result, nil
}

func (store Store) saveModelCache(file modelCacheFile) error {
	home, err := store.ensureHome()
	if err != nil {
		return err
	}
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	if len(data) > maxModelCacheBytes {
		return errors.New("provider model cache exceeds 4 MiB")
	}
	temp, err := os.CreateTemp(home, ".models-cache-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), store.modelCachePath()); err != nil {
		return err
	}
	directory, err := os.Open(home)
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	return err
}

func (store Store) modelCachePath() string {
	home, err := filepath.Abs(store.Home)
	if err != nil {
		home = store.Home
	}
	return filepath.Join(home, "models-cache.json")
}

func validOptionalLimit(value *int64) bool {
	return value == nil || (*value > 0 && *value <= 10_000_000)
}
