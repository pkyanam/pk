package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Fingerprint binds a session to its selected provider without persisting or
// exposing literal credentials. The API key contributes only through SHA-256.
func (provider Provider) Fingerprint() string {
	key, _ := provider.APIKeyValue()
	keyHash := sha256.Sum256([]byte(key))
	base, _ := NormalizeBaseURL(provider.BaseURL)
	value := struct {
		ID                      string   `json:"id"`
		Protocol                Protocol `json:"protocol"`
		BaseURL                 string   `json:"base_url"`
		KeyHash                 string   `json:"key_hash"`
		DefaultModel            string   `json:"default_model"`
		DefaultEffort           string   `json:"default_effort"`
		SupportsReasoningEffort bool     `json:"supports_reasoning_effort"`
	}{provider.ID, provider.Protocol, base, hex.EncodeToString(keyHash[:]), provider.DefaultModel, provider.DefaultEffort, provider.SupportsReasoningEffort}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
