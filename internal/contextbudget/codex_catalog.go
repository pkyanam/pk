package contextbudget

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const maxCodexCatalogBytes = 8 << 20

// ReadCodexModelCache reads only model-limit metadata from Codex's existing
// local model catalog. It deliberately ignores identity, prompts, and all
// other cache fields. The caller owns freshness policy.
func ReadCodexModelCache(path, modelID string) (Limits, bool, error) {
	modelID = strings.TrimSpace(modelID)
	if path == "" || modelID == "" {
		return Limits{}, false, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Limits{}, false, nil
	}
	if err != nil {
		return Limits{}, false, fmt.Errorf("inspect Codex model catalog: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxCodexCatalogBytes {
		return Limits{}, false, errors.New("Codex model catalog must be a regular file no larger than 8 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return Limits{}, false, fmt.Errorf("open Codex model catalog: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCodexCatalogBytes+1))
	if err != nil {
		return Limits{}, false, fmt.Errorf("read Codex model catalog: %w", err)
	}
	if len(data) > maxCodexCatalogBytes {
		return Limits{}, false, errors.New("Codex model catalog exceeds 8 MiB")
	}
	var catalog struct {
		FetchedAt string `json:"fetched_at"`
		Models    []struct {
			Slug                          string `json:"slug"`
			ContextWindow                 *int64 `json:"context_window"`
			MaxContextWindow              *int64 `json:"max_context_window"`
			EffectiveContextWindowPercent *int64 `json:"effective_context_window_percent"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return Limits{}, false, errors.New("Codex model catalog is invalid")
	}
	var fetchedAt time.Time
	if catalog.FetchedAt != "" {
		fetchedAt, _ = time.Parse(time.RFC3339Nano, catalog.FetchedAt)
	}
	for _, model := range catalog.Models {
		if model.Slug != modelID {
			continue
		}
		context := positive(model.ContextWindow)
		if context == nil {
			context = positive(model.MaxContextWindow)
		}
		if context == nil {
			return Limits{}, false, nil
		}
		if *context > 10_000_000 {
			return Limits{}, false, errors.New("Codex model catalog has an unreasonable context limit")
		}
		percent := int64(95)
		if model.EffectiveContextWindowPercent != nil {
			percent = *model.EffectiveContextWindowPercent
		}
		if percent < 1 || percent > 100 {
			return Limits{}, false, errors.New("Codex model catalog has an invalid effective context percentage")
		}
		usable := *context * percent / 100
		return Limits{
			ContextTokens:         context,
			InputTokens:           int64ptr(usable),
			Source:                SourceCodexLocalCatalog,
			FetchedAt:             fetchedAt,
			InputIncludesHeadroom: true,
		}, true, nil
	}
	return Limits{}, false, nil
}

func positive(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	return clone(value)
}
