package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const anthropicVersion = "2023-06-01"
const defaultAnthropicMaxTokens = 4096

type anthropicAdapter struct {
	provider Provider
	apiKey   string
	endpoint string
	http     *http.Client
}

func newAnthropicAdapter(provider Provider, key, baseURL string) *anthropicAdapter {
	return &anthropicAdapter{provider: provider, apiKey: key, endpoint: baseURL + "/messages", http: newProviderHTTPClient()}
}

func (adapter *anthropicAdapter) Close() error {
	if adapter.http != nil {
		adapter.http.CloseIdleConnections()
	}
	return nil
}

func (adapter *anthropicAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	encodedMessages, system, err := encodeAnthropicMessages(request.Input)
	if err != nil {
		return llm.Response{}, err
	}
	maxTokens := int64(defaultAnthropicMaxTokens)
	if request.Model.MaxOutputTokens != nil {
		maxTokens = *request.Model.MaxOutputTokens
	}
	if maxTokens <= 0 {
		return llm.Response{}, errors.New("Anthropic max output tokens must be positive")
	}
	body := map[string]any{"model": request.Model.ID, "messages": encodedMessages, "max_tokens": maxTokens, "stream": true}
	if system != "" {
		body["system"] = system
	}
	if anthropicAutomaticCaching(adapter.provider.BaseURL) {
		body["cache_control"] = map[string]string{"type": "ephemeral"}
	}
	if len(request.Tools) != 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Type != llm.ToolFunction {
				continue
			}
			if tool.Name == "" || len(tool.Name) > 128 {
				return llm.Response{}, errors.New("Anthropic tool name is empty or exceeds 128 bytes")
			}
			schema := tool.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": schema})
		}
		if len(tools) > 0 {
			body["tools"] = tools
			body["tool_choice"] = map[string]string{"type": "auto"}
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("encode Anthropic Messages request: %w", err)
	}
	if len(encoded) > maxRequestBytes {
		return llm.Response{}, fmt.Errorf("Anthropic Messages request exceeds %d bytes", maxRequestBytes)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, adapter.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return llm.Response{}, fmt.Errorf("create Anthropic Messages request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("anthropic-version", anthropicVersion)
	if adapter.apiKey != "" {
		httpRequest.Header.Set("x-api-key", adapter.apiKey)
	}
	response, err := adapter.http.Do(httpRequest)
	if err != nil {
		return llm.Response{}, fmt.Errorf("Anthropic Messages request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return llm.Response{}, fmt.Errorf("Anthropic Messages endpoint returned HTTP %d", response.StatusCode)
	}
	return decodeAnthropicStream(ctx, response.Body)
}

func anthropicAutomaticCaching(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	return err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "api.anthropic.com")
}

func listAnthropicModels(ctx context.Context, baseURL, key string) ([]Model, error) {
	transport := providerTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	models := make([]Model, 0)
	seenIDs := make(map[string]bool)
	seenCursors := make(map[string]bool)
	cursor := ""
	for page := 0; page < 100; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		u, err := url.Parse(baseURL + "/models")
		if err != nil {
			return nil, errors.New("invalid Anthropic models endpoint")
		}
		query := u.Query()
		query.Set("limit", "100")
		if cursor != "" {
			query.Set("after_id", cursor)
		}
		u.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create Anthropic models request: %w", err)
		}
		req.Header.Set("anthropic-version", anthropicVersion)
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request Anthropic models: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("Anthropic models endpoint returned HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponseBytes+1))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read Anthropic models: %w", err)
		}
		if len(data) > maxModelsResponseBytes {
			return nil, errors.New("Anthropic models response exceeds 1 MiB")
		}
		var result struct {
			Data []struct {
				ID             string `json:"id"`
				MaxInputTokens *int64 `json:"max_input_tokens"`
				MaxTokens      *int64 `json:"max_tokens"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, errors.New("Anthropic models response is invalid JSON")
		}
		for _, item := range result.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" || len(id) > 256 || strings.ContainsAny(id, "\r\n\x00") || seenIDs[id] {
				continue
			}
			seenIDs[id] = true
			model := Model{ID: id, Object: "model", OwnedBy: "anthropic"}
			model.InputTokens = clonePositiveLimit(item.MaxInputTokens)
			model.OutputTokens = clonePositiveLimit(item.MaxTokens)
			if model.InputTokens != nil || model.OutputTokens != nil {
				model.LimitsSource = "provider_reported"
			}
			models = append(models, model)
		}
		if !result.HasMore {
			sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
			return models, nil
		}
		next := strings.TrimSpace(result.LastID)
		if next == "" && len(result.Data) > 0 {
			next = result.Data[len(result.Data)-1].ID
		}
		if next == "" || seenCursors[next] {
			return nil, errors.New("Anthropic models pagination returned an invalid cursor")
		}
		seenCursors[next] = true
		cursor = next
	}
	return nil, errors.New("Anthropic models pagination exceeded 100 pages")
}

func encodeAnthropicMessages(input []llm.Item) ([]map[string]any, string, error) {
	messages := make([]map[string]any, 0, len(input))
	var system []string
	var pendingAssistant []map[string]any
	var pendingTools map[string]bool
	var pendingResults []map[string]any
	flushAssistant := func() {
		if len(pendingAssistant) > 0 {
			messages = append(messages, map[string]any{"role": "assistant", "content": pendingAssistant})
			pendingAssistant = nil
		}
	}
	flushResults := func() {
		if len(pendingResults) > 0 {
			messages = append(messages, map[string]any{"role": "user", "content": pendingResults})
			pendingResults = nil
		}
	}
	for _, item := range input {
		switch item.Type {
		case llm.ItemReasoning:
			// Provider-specific reasoning blocks are not portable across providers.
			continue
		case llm.ItemMessage:
			message, ok := item.Data.(llm.Message)
			if !ok {
				return nil, "", errors.New("Anthropic input contains an invalid message")
			}
			switch message.Role {
			case llm.RoleSystem:
				flushAssistant()
				flushResults()
				system = append(system, message.Text)
				continue
			case llm.RoleAssistant:
				if len(pendingTools) != 0 {
					return nil, "", errors.New("Anthropic input interleaves assistant content before tool results")
				}
				flushResults()
				pendingAssistant = append(pendingAssistant, map[string]any{"type": "text", "text": message.Text})
				continue
			case llm.RoleUser:
			default:
				return nil, "", fmt.Errorf("Anthropic Messages does not support role %q", message.Role)
			}
			if pendingTools != nil && len(pendingTools) != 0 {
				return nil, "", errors.New("Anthropic input interleaves a message before tool results")
			}
			flushAssistant()
			flushResults()
			messages = append(messages, map[string]any{"role": string(message.Role), "content": message.Text})
		case llm.ItemToolCall:
			call, ok := item.Data.(llm.ToolCall)
			if !ok || call.CallID == "" || call.Name == "" || len(call.Arguments) > maxToolArgumentsBytes || !json.Valid([]byte(call.Arguments)) {
				return nil, "", errors.New("Anthropic input contains an invalid tool call")
			}
			var inputValue any
			if err := json.Unmarshal([]byte(call.Arguments), &inputValue); err != nil {
				return nil, "", errors.New("Anthropic tool arguments are invalid JSON")
			}
			if _, ok := inputValue.(map[string]any); !ok {
				return nil, "", errors.New("Anthropic tool arguments must be a JSON object")
			}
			if pendingTools == nil {
				pendingTools = make(map[string]bool)
			}
			if pendingTools[call.CallID] {
				return nil, "", errors.New("Anthropic input contains duplicate tool call IDs")
			}
			flushResults()
			pendingTools[call.CallID] = true
			pendingAssistant = append(pendingAssistant, map[string]any{"type": "tool_use", "id": call.CallID, "name": call.Name, "input": inputValue})
		case llm.ItemToolResult:
			result, ok := item.Data.(llm.ToolResult)
			if !ok || result.CallID == "" || pendingTools == nil || !pendingTools[result.CallID] {
				return nil, "", errors.New("Anthropic input contains an unmatched tool result")
			}
			if len(pendingResults) == 0 {
				flushAssistant()
			}
			content, err := anthropicToolResultContent(result.Output)
			if err != nil {
				return nil, "", err
			}
			pendingResults = append(pendingResults, map[string]any{"type": "tool_result", "tool_use_id": result.CallID, "content": content})
			delete(pendingTools, result.CallID)
			if len(pendingTools) == 0 {
				flushResults()
			}
		default:
			return nil, "", fmt.Errorf("unsupported Anthropic input item %q", item.Type)
		}
	}
	flushAssistant()
	flushResults()
	if len(pendingTools) != 0 {
		return nil, "", errors.New("Anthropic input ended before all tool results")
	}
	return messages, strings.Join(system, "\n\n"), nil
}

func anthropicToolResultContent(outputs []llm.ToolResultOutput) ([]map[string]any, error) {
	content := make([]map[string]any, 0, len(outputs))
	for _, output := range outputs {
		switch output.Kind {
		case llm.ToolResultText:
			content = append(content, map[string]any{"type": "text", "text": output.Value})
		case llm.ToolResultImage:
			mediaType, encoded, ok := strings.Cut(strings.TrimPrefix(output.Value, "data:"), ";base64,")
			if !ok || (mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/gif" && mediaType != "image/webp") {
				return nil, errors.New("Anthropic tool result image must be a supported base64 data URL")
			}
			if len(encoded) > maxImageReferenceBytes {
				return nil, errors.New("Anthropic tool result image exceeds size limit")
			}
			size, err := io.Copy(io.Discard, io.LimitReader(base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)), maxDecodedImageBytes+1))
			if err != nil {
				return nil, errors.New("Anthropic tool result image is invalid base64")
			}
			if size > maxDecodedImageBytes {
				return nil, errors.New("Anthropic tool result image exceeds decoded size limit")
			}
			content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": encoded}})
		default:
			return nil, fmt.Errorf("unsupported Anthropic tool result kind %q", output.Kind)
		}
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	return content, nil
}

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Message *struct {
		ID    string `json:"id"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Index        int `json:"index"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *struct {
		InputTokens              *int64 `json:"input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicToolCall struct {
	id, name  string
	arguments strings.Builder
}

type anthropicOutputBlock struct {
	typeName string
	text     strings.Builder
	tool     *anthropicToolCall
}

func decodeAnthropicStream(ctx context.Context, source io.Reader) (llm.Response, error) {
	observer, _ := ctx.Value(observerKey{}).(func(Event))
	response := llm.Response{Stop: llm.StopComplete}
	blocks := map[int]*anthropicOutputBlock{}
	usageFields := make(map[string]json.RawMessage)
	var uncachedInputTokens, cacheReadInputTokens, cacheCreationInputTokens int64
	scanner := bufio.NewScanner(io.LimitReader(source, maxResponseBytes+1))
	scanner.Buffer(make([]byte, 4096), 5<<20)
	var eventName string
	bytesRead := 0
	messageStopped := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return llm.Response{}, err
		}
		line := scanner.Text()
		bytesRead += len(line) + 1
		if bytesRead > maxResponseBytes {
			return llm.Response{}, errors.New("Anthropic stream exceeded 16 MiB")
		}
		if line == "" {
			eventName = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			return llm.Response{}, errors.New("Anthropic stream contained invalid JSON")
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return llm.Response{}, errors.New("Anthropic stream contained invalid JSON")
		}
		if event.Type == "" {
			event.Type = eventName
		}
		if event.Type == "error" {
			if event.Error != nil {
				return llm.Response{}, fmt.Errorf("Anthropic stream error (%s)", safeAnthropicCode(event.Error.Type))
			}
			return llm.Response{}, errors.New("Anthropic stream error")
		}
		switch event.Type {
		case "message_start":
			if event.Message != nil {
				response.ID = event.Message.ID
				response.Usage.InputTokens = event.Message.Usage.InputTokens
				uncachedInputTokens = event.Message.Usage.InputTokens
				response.Usage.CachedInputTokens = event.Message.Usage.CacheReadInputTokens
				cacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
				response.Usage.CacheWriteInputTokens = event.Message.Usage.CacheCreationInputTokens
				cacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
			}
			if raw, ok := envelope["message"]; ok {
				var messageRaw map[string]json.RawMessage
				if json.Unmarshal(raw, &messageRaw) == nil {
					mergeAnthropicUsage(usageFields, messageRaw["usage"])
				}
			}
		case "content_block_start":
			if _, exists := blocks[event.Index]; exists {
				return llm.Response{}, errors.New("Anthropic stream reused a content block index")
			}
			block := &anthropicOutputBlock{typeName: event.ContentBlock.Type}
			if event.ContentBlock.Type == "tool_use" {
				if event.ContentBlock.ID == "" || event.ContentBlock.Name == "" {
					return llm.Response{}, errors.New("Anthropic stream returned an invalid tool call")
				}
				if len(event.ContentBlock.Input) > maxToolArgumentsBytes {
					return llm.Response{}, errors.New("Anthropic tool arguments exceed 4 MiB")
				}
				call := &anthropicToolCall{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
				if len(event.ContentBlock.Input) > 0 && string(event.ContentBlock.Input) != "{}" && string(event.ContentBlock.Input) != "null" {
					call.arguments.Write(event.ContentBlock.Input)
				}
				for _, existing := range blocks {
					if existing.tool != nil && existing.tool.id == call.id {
						return llm.Response{}, errors.New("Anthropic stream returned duplicate tool call IDs")
					}
				}
				block.tool = call
				if observer != nil {
					observer(Event{Kind: "tool_call_started", ToolIndex: event.Index, ToolName: call.name})
				}
			}
			blocks[event.Index] = block
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				block := blocks[event.Index]
				if block == nil || block.typeName != "text" {
					return llm.Response{}, errors.New("Anthropic text delta has no active text block")
				}
				block.text.WriteString(event.Delta.Text)
				if observer != nil && event.Delta.Text != "" {
					observer(Event{Kind: "assistant_delta", Text: event.Delta.Text})
				}
			case "input_json_delta":
				block := blocks[event.Index]
				var call *anthropicToolCall
				if block != nil {
					call = block.tool
				}
				if call == nil || call.arguments.Len()+len(event.Delta.PartialJSON) > maxToolArgumentsBytes {
					return llm.Response{}, errors.New("Anthropic tool arguments exceed 4 MiB or have no active tool")
				}
				call.arguments.WriteString(event.Delta.PartialJSON)
				if observer != nil {
					observer(Event{Kind: "tool_arguments_progress", ToolIndex: event.Index, ArgumentsBytes: call.arguments.Len()})
				}
			}
		case "message_delta":
			switch event.Delta.StopReason {
			case "max_tokens":
				response.Stop = llm.StopMaxOutputTokens
			case "refusal":
				response.Stop = llm.StopRefused
				response.Failure = &llm.Failure{Code: "refusal", Message: "provider refused the request"}
			}
			if event.Usage != nil {
				if event.Usage.InputTokens != nil {
					response.Usage.InputTokens = *event.Usage.InputTokens
					uncachedInputTokens = *event.Usage.InputTokens
				}
				if event.Usage.CacheReadInputTokens != nil {
					response.Usage.CachedInputTokens = *event.Usage.CacheReadInputTokens
					cacheReadInputTokens = *event.Usage.CacheReadInputTokens
				}
				if event.Usage.CacheCreationInputTokens != nil {
					response.Usage.CacheWriteInputTokens = *event.Usage.CacheCreationInputTokens
					cacheCreationInputTokens = *event.Usage.CacheCreationInputTokens
				}
				if event.Usage.OutputTokens != nil {
					response.Usage.OutputTokens = *event.Usage.OutputTokens
				}
				mergeAnthropicUsage(usageFields, envelope["usage"])
			}
		case "message_stop":
			messageStopped = true
		case "ping", "content_block_stop", "message_start_delta":
		default:
			// Anthropic may add event kinds; unknown metadata is ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		return llm.Response{}, fmt.Errorf("read Anthropic stream: %w", err)
	}
	if !messageStopped {
		return llm.Response{}, errors.New("Anthropic stream ended before message_stop")
	}
	response.Usage.InputTokens = uncachedInputTokens + cacheReadInputTokens + cacheCreationInputTokens
	if len(usageFields) > 0 {
		encoded, _ := json.Marshal(usageFields)
		response.Usage.Raw = jsontext.Value(encoded)
	}
	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		block := blocks[index]
		switch block.typeName {
		case "text":
			if block.text.Len() > 0 {
				response.Output = append(response.Output, llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: block.text.String()}})
			}
		case "tool_use":
			call := block.tool
			arguments := call.arguments.String()
			if arguments == "" {
				arguments = "{}"
			}
			var inputValue map[string]json.RawMessage
			if !json.Valid([]byte(arguments)) || json.Unmarshal([]byte(arguments), &inputValue) != nil || inputValue == nil {
				return llm.Response{}, errors.New("Anthropic stream returned invalid tool JSON")
			}
			response.Output = append(response.Output, llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: call.id, Name: call.name, Arguments: arguments}})
		}
	}
	if len(response.Output) == 0 && response.Failure == nil {
		return llm.Response{}, errors.New("Anthropic stream ended without assistant content or tool calls")
	}
	return response, nil
}

func mergeAnthropicUsage(target map[string]json.RawMessage, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return
	}
	for key, value := range fields {
		target[key] = append(json.RawMessage(nil), value...)
	}
}

func safeAnthropicCode(value string) string {
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "\r\n\x00") {
		return "unknown"
	}
	return value
}
