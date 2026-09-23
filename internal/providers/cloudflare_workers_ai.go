package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCloudflareModelPages = 20
const cloudflareModelsPerPage = 100
const maxCloudflareModelsBody = 2 << 20

var cloudflareAccountIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

func cloudflareAccountID(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "api.cloudflare.com" {
		return "", errors.New("Workers AI requires the official HTTPS api.cloudflare.com endpoint")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 6 || parts[0] != "client" || parts[1] != "v4" || parts[2] != "accounts" || parts[4] != "ai" || parts[5] != "v1" {
		return "", errors.New("Workers AI endpoint must include /client/v4/accounts/{account_id}/ai/v1")
	}
	if !cloudflareAccountIDPattern.MatchString(parts[3]) {
		return "", errors.New("Cloudflare account ID must be 32 hexadecimal characters")
	}
	return strings.ToLower(parts[3]), nil
}

func listCloudflareWorkersAIModels(ctx context.Context, baseURL, key string) ([]Model, error) {
	if _, err := cloudflareAccountID(baseURL); err != nil {
		return nil, err
	}
	transport := providerTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return listCloudflareWorkersAIModelsWithClient(ctx, baseURL, key, client)
}

func listCloudflareWorkersAIModelsWithClient(ctx context.Context, baseURL, key string, client *http.Client) ([]Model, error) {
	accountID, err := cloudflareAccountID(baseURL)
	if err != nil {
		return nil, err
	}
	endpoint := "https://api.cloudflare.com/client/v4/accounts/" + accountID + "/ai/models/search"
	seen := make(map[string]bool)
	var models []Model
	for page := 1; page <= maxCloudflareModelPages; page++ {
		query := url.Values{
			"task": {"Text Generation"}, "hide_experimental": {"false"},
			"per_page": {fmt.Sprint(cloudflareModelsPerPage)}, "page": {fmt.Sprint(page)},
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
		if err != nil {
			return nil, errors.New("could not create Workers AI model discovery request")
		}
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("Workers AI model discovery request failed: %w", err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return nil, fmt.Errorf("Workers AI model discovery returned HTTP %d", response.StatusCode)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxCloudflareModelsBody+1))
		response.Body.Close()
		if readErr != nil {
			return nil, errors.New("could not read Workers AI model catalog")
		}
		if len(data) > maxCloudflareModelsBody {
			return nil, errors.New("Workers AI model catalog response exceeds 2 MiB")
		}
		var envelope struct {
			Success *bool             `json:"success"`
			Result  []json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil || (envelope.Success != nil && !*envelope.Success) {
			return nil, errors.New("Workers AI model catalog response is invalid")
		}
		for _, raw := range envelope.Result {
			model, ok := decodeCloudflareModel(raw)
			if !ok || seen[model.ID] {
				continue
			}
			seen[model.ID] = true
			models = append(models, model)
		}
		if len(envelope.Result) < cloudflareModelsPerPage {
			break
		}
	}
	return models, nil
}

func decodeCloudflareModel(data []byte) (Model, bool) {
	var entry struct {
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		Task         json.RawMessage `json:"task"`
		Properties   json.RawMessage `json:"properties"`
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(data, &entry) != nil {
		return Model{}, false
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
		return Model{}, false
	}
	taskName := cloudflareTaskName(entry.Task)
	if taskName != "" && !strings.EqualFold(taskName, "Text Generation") {
		return Model{}, false
	}
	model := Model{ID: name, Object: "workers_ai", Task: taskName, Description: boundedText(entry.Description, 4096)}
	model.Capabilities = cloudflareCapabilities(entry.Capabilities, entry.Properties)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	model.ContextTokens = firstPositiveInteger(fields, "context_tokens", "context_window", "context_length")
	model.InputTokens = firstPositiveInteger(fields, "input_tokens", "max_input_tokens")
	model.OutputTokens = firstPositiveInteger(fields, "output_tokens", "max_output_tokens", "max_tokens")
	var propertyFields map[string]json.RawMessage
	if json.Unmarshal(entry.Properties, &propertyFields) == nil {
		if model.ContextTokens == nil {
			model.ContextTokens = firstPositiveInteger(propertyFields, "context_tokens", "context_window", "context_length")
		}
		if model.InputTokens == nil {
			model.InputTokens = firstPositiveInteger(propertyFields, "input_tokens", "max_input_tokens")
		}
		if model.OutputTokens == nil {
			model.OutputTokens = firstPositiveInteger(propertyFields, "output_tokens", "max_output_tokens", "max_tokens")
		}
	}
	if model.ContextTokens == nil || model.InputTokens == nil || model.OutputTokens == nil {
		var propertyList []json.RawMessage
		if json.Unmarshal(entry.Properties, &propertyList) == nil {
			for _, rawProperty := range propertyList {
				var values map[string]json.RawMessage
				if json.Unmarshal(rawProperty, &values) != nil {
					continue
				}
				if model.ContextTokens == nil {
					model.ContextTokens = firstPositiveInteger(values, "context_tokens", "context_window", "context_length", "max_context_length", "max_model_len")
				}
				if model.InputTokens == nil {
					model.InputTokens = firstPositiveInteger(values, "input_tokens", "max_input_tokens", "max_prompt_tokens")
				}
				if model.OutputTokens == nil {
					model.OutputTokens = firstPositiveInteger(values, "output_tokens", "max_output_tokens", "max_tokens")
				}
				propertyID := firstString(values, "property_id", "propertyId", "key", "name", "id")
				propertyValue, ok := values["value"]
				if !ok {
					propertyValue, ok = values["values"]
				}
				if !ok {
					continue
				}
				limit := positiveIntegerValue(propertyValue)
				if limit == nil {
					continue
				}
				switch normalizeCloudflarePropertyID(propertyID) {
				case "context_tokens", "context_window", "context_length", "max_context_length", "max_model_len":
					if model.ContextTokens == nil {
						model.ContextTokens = limit
					}
				case "input_tokens", "max_input_tokens", "max_prompt_tokens":
					if model.InputTokens == nil {
						model.InputTokens = limit
					}
				case "output_tokens", "max_output_tokens", "max_tokens":
					if model.OutputTokens == nil {
						model.OutputTokens = limit
					}
				}
			}
		}
	}
	if model.ContextTokens != nil || model.InputTokens != nil || model.OutputTokens != nil {
		model.LimitsSource = "provider_reported"
	}
	return model, true
}

func firstString(fields map[string]json.RawMessage, names ...string) string {
	for _, name := range names {
		var value string
		if json.Unmarshal(fields[name], &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeCloudflarePropertyID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	return value
}

func positiveIntegerValue(raw json.RawMessage) *int64 {
	var number int64
	if json.Unmarshal(raw, &number) == nil && number > 0 {
		return &number
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err == nil && parsed > 0 {
			return &parsed
		}
	}
	return nil
}

func cloudflareTaskName(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return boundedText(text, 128)
	}
	var task struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &task) == nil {
		return boundedText(task.Name, 128)
	}
	return ""
}

func cloudflareCapabilities(direct, raw json.RawMessage) []string {
	var names []string
	var directStrings []string
	if json.Unmarshal(direct, &directStrings) == nil {
		names = append(names, directStrings...)
	} else {
		var directRecords []json.RawMessage
		if json.Unmarshal(direct, &directRecords) == nil {
			for _, record := range directRecords {
				var value struct {
					Name       string `json:"name"`
					ID         string `json:"id"`
					PropertyID string `json:"property_id"`
					Label      string `json:"label"`
				}
				if json.Unmarshal(record, &value) == nil {
					names = append(names, value.Name, value.ID, value.PropertyID, value.Label)
				}
			}
		}
	}
	// Catalog versions have represented badges as either a string list or
	// property records. Accept only the documented function-calling badge.
	var properties []struct {
		Name       string `json:"name"`
		ID         string `json:"id"`
		PropertyID string `json:"property_id"`
		Label      string `json:"label"`
	}
	_ = json.Unmarshal(raw, &properties)
	for _, property := range properties {
		names = append(names, property.Name, property.ID, property.PropertyID, property.Label)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		for key, value := range object {
			var enabled bool
			if json.Unmarshal(value, &enabled) == nil && enabled {
				names = append(names, key)
			}
		}
	}
	for _, name := range names {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(name), "_", " "), "-", " "))
		if normalized == "function calling" || normalized == "tool calling" || normalized == "function call" {
			return []string{"function_calling"}
		}
	}
	return nil
}

func boundedText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		value = value[:max]
		for !utf8.ValidString(value) && len(value) > 0 {
			value = value[:len(value)-1]
		}
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, value)
}
