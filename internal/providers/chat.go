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
	"strconv"
	"strings"

	"github.com/pkyanam/pk/internal/modelstream"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const maxRequestBytes = 32 << 20
const maxResponseBytes = 16 << 20
const maxToolArgumentsBytes = 4 << 20

type Event struct {
	Kind           string
	Text           string
	ToolIndex      int
	ToolName       string
	ArgumentsBytes int
	Bytes          int
}

type observerKey struct{}

func WithObserver(ctx context.Context, observer func(Event)) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, observerKey{}, observer)
}

type chatAdapter struct {
	provider Provider
	apiKey   string
	endpoint string
	http     *http.Client
}

func newChatAdapter(provider Provider, key, baseURL string) *chatAdapter {
	return &chatAdapter{provider: provider, apiKey: key, endpoint: baseURL + "/chat/completions", http: newProviderHTTPClient()}
}

func (adapter *chatAdapter) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (result llm.Response, retErr error) {
	observed, observeErr := modelstream.BeginObservedCall(ctx, "", 1)
	if observeErr != nil {
		return llm.Response{}, observeErr
	}
	defer func() { observed.Finish(retErr) }()
	previousObserver, _ := ctx.Value(observerKey{}).(func(Event))
	argumentBytes := make(map[int]int)
	ctx = WithObserver(ctx, func(event Event) {
		if previousObserver != nil {
			previousObserver(event)
		}
		switch event.Kind {
		case "assistant_delta":
			observed.Emit(modelstream.Event{Kind: modelstream.EventAssistantDelta, Text: event.Text, Bytes: len(event.Text)})
		case "reasoning_progress":
			observed.Emit(modelstream.Event{Kind: modelstream.EventReasoningProgress, Bytes: event.Bytes})
		case "tool_call_started":
			observed.Emit(modelstream.Event{Kind: modelstream.EventToolCallStarted, ItemID: strconv.Itoa(event.ToolIndex), ToolName: event.ToolName})
		case "tool_arguments_progress":
			itemID := strconv.Itoa(event.ToolIndex)
			delta := event.ArgumentsBytes - argumentBytes[event.ToolIndex]
			argumentBytes[event.ToolIndex] = event.ArgumentsBytes
			if delta > 0 {
				observed.Emit(modelstream.Event{Kind: modelstream.EventToolArgumentsProgress, ItemID: itemID, Bytes: delta})
			}
		}
	})
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	messages, err := encodeMessages(request.Input)
	if err != nil {
		return llm.Response{}, err
	}
	body := map[string]any{"model": request.Model.ID, "messages": messages, "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if request.Model.MaxOutputTokens != nil {
		if adapter.provider.Protocol == ProtocolCloudflareWorkersAI {
			body["max_tokens"] = *request.Model.MaxOutputTokens
		} else {
			body["max_completion_tokens"] = *request.Model.MaxOutputTokens
		}
	}
	if adapter.provider.SupportsReasoningEffort && request.Model.ReasoningEffort != "" {
		if !request.Model.ReasoningEffort.Valid() {
			return llm.Response{}, fmt.Errorf("unsupported reasoning effort %q", request.Model.ReasoningEffort)
		}
		body["reasoning_effort"] = string(request.Model.ReasoningEffort)
	}
	if len(request.Tools) != 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, item := range request.Tools {
			if item.Type != llm.ToolFunction {
				continue
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": item.Name, "description": item.Description, "parameters": item.Parameters,
			}})
		}
		if len(tools) != 0 {
			body["tools"] = tools
			body["tool_choice"] = "auto"
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("encode chat completion request: %w", err)
	}
	if len(encoded) > maxRequestBytes {
		return llm.Response{}, fmt.Errorf("chat completion request exceeds %d bytes", maxRequestBytes)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, adapter.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return llm.Response{}, fmt.Errorf("create chat completion request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if adapter.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+adapter.apiKey)
	}
	response, err := adapter.http.Do(httpRequest)
	if err != nil {
		return llm.Response{}, fmt.Errorf("chat completion request failed: %w", err)
	}
	defer response.Body.Close()
	observed.Emit(modelstream.Event{Kind: modelstream.EventResponseStarted, Status: response.StatusCode})
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("chat completion endpoint returned HTTP %d", response.StatusCode)
		if adapter.provider.Protocol == ProtocolCloudflareWorkersAI && len(request.Tools) > 0 && (response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnprocessableEntity) {
			message += "; this Workers AI model may not support function calling—choose a model marked for function calling in the model catalog"
		}
		return llm.Response{}, errors.New(message)
	}
	return decodeChatStream(ctx, response.Body)
}

func (adapter *chatAdapter) Close() error {
	if adapter.http != nil {
		adapter.http.CloseIdleConnections()
	}
	return nil
}

func encodeMessages(input []llm.Item) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(input))
	var deferredImages []map[string]any
	var pendingAssistantCalls []map[string]any
	pendingToolResults := make(map[string]bool)
	flushAssistantCalls := func() {
		if len(pendingAssistantCalls) == 0 {
			return
		}
		messages = append(messages, map[string]any{"role": "assistant", "tool_calls": pendingAssistantCalls})
		pendingAssistantCalls = nil
	}
	flushImages := func() {
		if len(deferredImages) == 0 {
			return
		}
		messages = append(messages, map[string]any{"role": "user", "content": deferredImages})
		deferredImages = nil
	}
	for _, item := range input {
		if item.Type == llm.ItemReasoning {
			continue
		}
		if len(deferredImages) > 0 && len(pendingToolResults) > 0 && item.Type != llm.ItemToolCall && item.Type != llm.ItemToolResult {
			return nil, errors.New("chat completion input interleaves messages before all parallel tool results")
		}
		if item.Type != llm.ItemToolCall {
			flushAssistantCalls()
		}
		// Chat Completions only accepts text parts for tool-role messages. Keep
		// every parallel tool response adjacent, then add image outputs as a
		// separate user message after the entire result group.
		if item.Type != llm.ItemToolResult && len(deferredImages) > 0 && len(pendingToolResults) == 0 {
			flushImages()
		}
		switch item.Type {
		case llm.ItemMessage:
			message, ok := item.Data.(llm.Message)
			if !ok {
				return nil, errors.New("chat completion input contains an invalid message")
			}
			role := string(message.Role)
			if role != "system" && role != "user" && role != "assistant" {
				return nil, fmt.Errorf("chat completions do not support message role %q", role)
			}
			messages = append(messages, map[string]any{"role": role, "content": message.Text})
		case llm.ItemToolCall:
			call, ok := item.Data.(llm.ToolCall)
			if !ok || call.CallID == "" || call.Name == "" || !json.Valid([]byte(call.Arguments)) {
				return nil, errors.New("chat completion input contains an invalid tool call")
			}
			pendingToolResults[call.CallID] = true
			pendingAssistantCalls = append(pendingAssistantCalls, map[string]any{
				"id": call.CallID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments},
			})
		case llm.ItemToolResult:
			result, ok := item.Data.(llm.ToolResult)
			if !ok || result.CallID == "" {
				return nil, errors.New("chat completion input contains an invalid tool result")
			}
			delete(pendingToolResults, result.CallID)
			var text strings.Builder
			for _, output := range result.Output {
				switch output.Kind {
				case llm.ToolResultText:
					text.WriteString(output.Value)
					text.WriteByte('\n')
				case llm.ToolResultImage:
					if err := validateImageReference(output.Value); err != nil {
						return nil, fmt.Errorf("chat completion tool result contains an unsupported image: %w", err)
					}
					deferredImages = append(deferredImages,
						map[string]any{"type": "text", "text": "Image output from tool call " + result.CallID},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": output.Value}},
					)
				default:
					return nil, fmt.Errorf("chat completion input contains unsupported tool result kind %q", output.Kind)
				}
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": result.CallID, "content": strings.TrimSpace(text.String())})
		case llm.ItemReasoning:
			// The generic Chat Completions protocol has no portable reasoning item.
			continue
		default:
			return nil, fmt.Errorf("unsupported chat completion input item %q", item.Type)
		}
	}
	flushAssistantCalls()
	if len(pendingToolResults) > 0 && len(deferredImages) > 0 {
		return nil, errors.New("chat completion input ended before all parallel tool results containing images")
	}
	flushImages()
	return messages, nil
}

const maxImageReferenceBytes = 24 << 20
const maxDecodedImageBytes = 16 << 20

func validateImageReference(value string) error {
	if value == "" || len(value) > maxImageReferenceBytes {
		return errors.New("image reference is empty or exceeds 24 MiB")
	}
	if strings.HasPrefix(value, "data:") {
		metadata, encoded, ok := strings.Cut(value, ",")
		if !ok || !strings.HasSuffix(metadata, ";base64") {
			return errors.New("data image must use a base64 data URL")
		}
		switch strings.TrimSuffix(strings.TrimPrefix(metadata, "data:"), ";base64") {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return errors.New("data image type is unsupported")
		}
		if encoded == "" {
			return errors.New("data image is empty")
		}
		decodedBytes, err := io.Copy(io.Discard, io.LimitReader(base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)), maxDecodedImageBytes+1))
		if err != nil {
			return errors.New("data image is not valid base64")
		}
		if decodedBytes > maxDecodedImageBytes {
			return errors.New("decoded data image exceeds 16 MiB")
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return errors.New("image reference must be an HTTP(S) URL or supported image data URL")
	}
	return nil
}

type chatChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Refusal          string `json:"refusal"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

type assembledToolCall struct {
	ID, Name, Arguments string
	Started             bool
}

func decodeChatStream(ctx context.Context, source io.Reader) (llm.Response, error) {
	observer, _ := ctx.Value(observerKey{}).(func(Event))
	response := llm.Response{Stop: llm.StopComplete}
	var content strings.Builder
	var refusal strings.Builder
	tools := map[int]*assembledToolCall{}
	finishReason := ""
	scanner := bufio.NewScanner(io.LimitReader(source, maxResponseBytes+1))
	scanner.Buffer(make([]byte, 4096), 2<<20)
	bytesRead := 0
	terminal := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return llm.Response{}, err
		}
		line := scanner.Text()
		bytesRead += len(line) + 1
		if bytesRead > maxResponseBytes {
			return llm.Response{}, errors.New("chat completion stream exceeded 16 MiB")
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			terminal = true
			break
		}
		if data == "" {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return llm.Response{}, fmt.Errorf("decode chat completion stream event: %w", err)
		}
		if response.ID == "" && chunk.ID != "" {
			response.ID = chunk.ID
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			var usage struct {
				PromptTokens     *int64 `json:"prompt_tokens"`
				CompletionTokens *int64 `json:"completion_tokens"`
				PromptDetails    *struct {
					CachedTokens *int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionDetails *struct {
					ReasoningTokens *int64 `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			}
			if err := json.Unmarshal(chunk.Usage, &usage); err != nil {
				return llm.Response{}, fmt.Errorf("decode chat completion usage: %w", err)
			}
			if usage.PromptTokens != nil {
				response.Usage.InputTokens = *usage.PromptTokens
			}
			if usage.CompletionTokens != nil {
				response.Usage.OutputTokens = *usage.CompletionTokens
			}
			if usage.PromptDetails != nil && usage.PromptDetails.CachedTokens != nil {
				response.Usage.CachedInputTokens = *usage.PromptDetails.CachedTokens
			}
			if usage.CompletionDetails != nil && usage.CompletionDetails.ReasoningTokens != nil {
				response.Usage.ReasoningTokens = *usage.CompletionDetails.ReasoningTokens
			}
			response.Usage.Raw = jsontext.Value(append([]byte(nil), chunk.Usage...))
		}
		for _, choice := range chunk.Choices {
			reasoningBytes := len(choice.Delta.ReasoningContent) + len(choice.Delta.Reasoning)
			if reasoningBytes > 0 {
				if observer != nil {
					observer(Event{Kind: "reasoning_progress", Bytes: reasoningBytes})
				}
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if observer != nil {
					observer(Event{Kind: "assistant_delta", Text: choice.Delta.Content})
				}
			}
			if choice.Delta.Refusal != "" {
				refusal.WriteString(choice.Delta.Refusal)
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := tools[delta.Index]
				if call == nil {
					call = &assembledToolCall{}
					tools[delta.Index] = call
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				if delta.Function.Name != "" {
					if !call.Started && observer != nil {
						observer(Event{Kind: "tool_call_started", ToolIndex: delta.Index, ToolName: delta.Function.Name})
						call.Started = true
					}
					call.Name += delta.Function.Name
				}
				if delta.Function.Arguments != "" {
					if len(call.Arguments)+len(delta.Function.Arguments) > maxToolArgumentsBytes {
						return llm.Response{}, errors.New("chat completion tool arguments exceed 4 MiB")
					}
					call.Arguments += delta.Function.Arguments
					if observer != nil {
						observer(Event{Kind: "tool_arguments_progress", ToolIndex: delta.Index, ArgumentsBytes: len(call.Arguments)})
					}
				}
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return llm.Response{}, fmt.Errorf("read chat completion stream: %w", err)
	}
	if !terminal {
		return llm.Response{}, errors.New("chat completion stream ended without [DONE]")
	}
	if content.Len() != 0 {
		response.Output = append(response.Output, llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: content.String()}})
	}
	if refusal.Len() != 0 {
		response.Stop = llm.StopRefused
		response.Failure = &llm.Failure{Code: "refusal", Message: refusal.String()}
	}
	indices := make([]int, 0, len(tools))
	for index := range tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := tools[index]
		if call.ID == "" || call.Name == "" || !json.Valid([]byte(call.Arguments)) {
			return llm.Response{}, fmt.Errorf("chat completion returned an invalid tool call at index %d", index)
		}
		response.Output = append(response.Output, llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: call.ID, Name: call.Name, Arguments: call.Arguments}})
	}
	if finishReason == "length" {
		response.Stop = llm.StopMaxOutputTokens
	} else if finishReason == "content_filter" {
		response.Stop = llm.StopRefused
		if response.Failure == nil {
			response.Failure = &llm.Failure{Code: "content_filter", Message: "provider filtered the response"}
		}
	}
	// Some OpenAI-compatible providers omit finish_reason on their final
	// chunk, so [DONE] remains the stream boundary. A metadata-only stream,
	// however, must not look like a successful empty assistant turn.
	if len(response.Output) == 0 && refusal.Len() == 0 && response.Stop == llm.StopComplete {
		return llm.Response{}, errors.New("chat completion stream ended without assistant content or tool calls")
	}
	return response, nil
}
