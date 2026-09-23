package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const maxModelsResponseBytes = 1 << 20

func decodeModels(body io.Reader) ([]Model, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxModelsResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read provider models: %w", err)
	}
	if len(data) > maxModelsResponseBytes {
		return nil, errors.New("provider models response exceeds 1 MiB")
	}
	var result struct {
		Data []Model `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("provider models response is invalid JSON")
	}
	seen := make(map[string]bool, len(result.Data))
	models := make([]Model, 0, len(result.Data))
	for _, model := range result.Data {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || len(model.ID) > 256 || strings.ContainsAny(model.ID, "\r\n\x00") || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
