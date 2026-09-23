// Package providers implements explicitly configured OpenAI-compatible model
// providers. It never reads credentials from CLI arguments or emits them in
// provider summaries/errors.
package providers

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/pkyanam/pk/internal/config"
)

type Protocol string

const (
	ProtocolResponses       Protocol = "responses"
	ProtocolChatCompletions Protocol = "chat_completions"
)

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Provider struct {
	ID                      string   `json:"id"`
	Protocol                Protocol `json:"protocol"`
	BaseURL                 string   `json:"base_url"`
	APIKeyEnv               string   `json:"api_key_env,omitempty"`
	APIKey                  string   `json:"api_key,omitempty"`
	DefaultModel            string   `json:"default_model,omitempty"`
	DefaultEffort           string   `json:"default_effort,omitempty"`
	SupportsReasoningEffort bool     `json:"supports_reasoning_effort,omitempty"`
}

type Summary struct {
	ID                      string   `json:"id"`
	Protocol                Protocol `json:"protocol"`
	BaseURL                 string   `json:"base_url"`
	APIKeyConfigured        bool     `json:"api_key_configured"`
	APIKeyEnv               string   `json:"api_key_env,omitempty"`
	DefaultModel            string   `json:"default_model,omitempty"`
	DefaultEffort           string   `json:"default_effort,omitempty"`
	SupportsReasoningEffort bool     `json:"supports_reasoning_effort"`
	IsDefault               bool     `json:"is_default"`
}

func (provider Provider) Validate() error {
	if !providerIDPattern.MatchString(provider.ID) {
		return fmt.Errorf("invalid provider ID %q", provider.ID)
	}
	if provider.Protocol != ProtocolResponses && provider.Protocol != ProtocolChatCompletions {
		return fmt.Errorf("provider %q protocol must be responses or chat_completions", provider.ID)
	}
	base, err := NormalizeBaseURL(provider.BaseURL)
	if err != nil {
		return fmt.Errorf("provider %q: %w", provider.ID, err)
	}
	_ = base
	if provider.APIKey != "" && provider.APIKeyEnv != "" {
		return fmt.Errorf("provider %q must use either an API key value or an environment variable, not both", provider.ID)
	}
	if strings.ContainsAny(provider.APIKey, "\r\n\x00") {
		return fmt.Errorf("provider %q API key contains invalid header characters", provider.ID)
	}
	if provider.APIKeyEnv != "" && !envNamePattern.MatchString(provider.APIKeyEnv) {
		return fmt.Errorf("provider %q has an invalid API key environment variable name", provider.ID)
	}
	if provider.DefaultModel != "" && strings.ContainsAny(provider.DefaultModel, "\r\n\x00") {
		return fmt.Errorf("provider %q has an invalid default model ID", provider.ID)
	}
	if provider.DefaultEffort != "" && !config.ValidEffort(provider.DefaultEffort) {
		return fmt.Errorf("provider %q has an unsupported default reasoning effort", provider.ID)
	}
	return nil
}

// NormalizeBaseURL treats a bare origin as the common /v1 API root and keeps
// an explicitly supplied versioned prefix intact.
func NormalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" {
		return "", errors.New("base URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", errors.New("base URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("base URL cannot include user information, query, or fragment")
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("HTTP is allowed only for localhost or loopback provider URLs; use HTTPS for remote providers")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		path = "/v1"
	} else if path == "/responses" || path == "/chat/completions" || path == "/models" {
		return "", errors.New("base URL must name the API root, not an endpoint")
	}
	parsed.Path = path
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (provider Provider) APIKeyValue() (string, error) {
	if provider.APIKey != "" {
		return provider.APIKey, nil
	}
	if provider.APIKeyEnv == "" {
		return "", nil
	}
	value := strings.TrimSpace(os.Getenv(provider.APIKeyEnv))
	if value == "" {
		return "", fmt.Errorf("provider %q requires environment variable %s", provider.ID, provider.APIKeyEnv)
	}
	return value, nil
}

// APIKeyWithLookup supports deterministic tests without reading a real secret.
func (provider Provider) APIKeyWithLookup(lookup func(string) (string, bool)) (string, error) {
	if provider.APIKey != "" {
		return provider.APIKey, nil
	}
	if provider.APIKeyEnv == "" {
		return "", nil
	}
	if lookup == nil {
		return "", errors.New("API key environment lookup is unavailable")
	}
	value, ok := lookup(provider.APIKeyEnv)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("provider %q requires environment variable %s", provider.ID, provider.APIKeyEnv)
	}
	return strings.TrimSpace(value), nil
}

func Summarize(provider Provider) Summary {
	base, _ := NormalizeBaseURL(provider.BaseURL)
	return Summary{
		ID: provider.ID, Protocol: provider.Protocol, BaseURL: base,
		APIKeyConfigured: provider.APIKey != "" || provider.APIKeyEnv != "",
		APIKeyEnv:        provider.APIKeyEnv, DefaultModel: provider.DefaultModel,
		DefaultEffort: provider.DefaultEffort, SupportsReasoningEffort: provider.SupportsReasoningEffort,
	}
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

type ModelsResult struct {
	Models []Model `json:"models"`
}
