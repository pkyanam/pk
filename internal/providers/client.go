package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

type Client struct {
	provider  Provider
	key       string
	adapter   llm.Adapter
	remote    *primitives.RemoteClient
	chat      *chatAdapter
	anthropic *anthropicAdapter
}

var _ llm.Adapter = (*Client)(nil)

func NewClient(provider Provider) (*Client, error) {
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	baseURL, _ := NormalizeBaseURL(provider.BaseURL)
	provider.BaseURL = baseURL
	key, err := provider.APIKeyValue()
	if err != nil {
		return nil, err
	}
	client := &Client{provider: provider, key: key}
	switch provider.Protocol {
	case ProtocolResponses:
		transport := &boundedResponseTransport{base: providerTransport(), maxBytes: maxResponseBytes}
		remote := primitives.NewRemoteClientWithHTTPClient(&http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		})
		headers := map[string][]string{"Content-Type": {"application/json"}}
		if key != "" {
			headers["Authorization"] = []string{"Bearer " + key}
		}
		adapter, err := responsesapi.NewAdapter(remote, responsesapi.Config{
			Endpoint: baseURL + "/responses",
			Headers:  headers,
			// External Responses endpoints are not assumed to support pk's
			// prompt-cache-key extension or Codex-specific headers.
		})
		if err != nil {
			_ = remote.Close()
			return nil, err
		}
		client.remote, client.adapter = remote, adapter
	case ProtocolChatCompletions:
		client.chat = newChatAdapter(provider, key, baseURL)
		client.adapter = client.chat
	case ProtocolAnthropic:
		client.anthropic = newAnthropicAdapter(provider, key, baseURL)
		client.adapter = client.anthropic
	default:
		return nil, errors.New("unsupported provider protocol")
	}
	return client, nil
}

type boundedResponseTransport struct {
	base     http.RoundTripper
	maxBytes int64
}

func (transport *boundedResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	response.Body = &boundedResponseBody{body: response.Body, maxBytes: transport.maxBytes}
	return response, nil
}

var errProviderResponseTooLarge = errors.New("provider response exceeded 16 MiB limit")

type boundedResponseBody struct {
	body     io.ReadCloser
	maxBytes int64
	read     int64
	checked  bool
}

func (body *boundedResponseBody) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if body.read < body.maxBytes {
		if int64(len(buffer)) > body.maxBytes-body.read {
			buffer = buffer[:body.maxBytes-body.read]
		}
		count, err := body.body.Read(buffer)
		body.read += int64(count)
		return count, err
	}
	if !body.checked {
		var probe [1]byte
		count, err := body.body.Read(probe[:])
		if count > 0 {
			body.checked = true
			return 0, errProviderResponseTooLarge
		}
		if err != nil {
			body.checked = true
			return 0, err
		}
		return 0, nil
	}
	return 0, io.EOF
}

func (body *boundedResponseBody) Close() error { return body.body.Close() }

func (client *Client) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	if client == nil || client.adapter == nil {
		return llm.Response{}, errors.New("provider client is unavailable")
	}
	if !client.provider.SupportsReasoningEffort {
		request.Model.ReasoningEffort = ""
	}
	response, err := client.adapter.Respond(ctx, request, options)
	if err != nil && client.key != "" {
		return llm.Response{}, redactedError{message: strings.ReplaceAll(err.Error(), client.key, "[redacted]"), cause: err}
	}
	if response.Failure != nil && client.key != "" {
		response.Failure.Message = strings.ReplaceAll(response.Failure.Message, client.key, "[redacted]")
	}
	return response, err
}

type redactedError struct {
	message string
	cause   error
}

func (err redactedError) Error() string { return err.message }
func (err redactedError) Unwrap() error { return err.cause }

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	if client.remote != nil {
		return client.remote.Close()
	}
	if client.chat != nil {
		return client.chat.Close()
	}
	if client.anthropic != nil {
		return client.anthropic.Close()
	}
	return nil
}

func (provider Provider) Models(ctx context.Context) ([]Model, error) {
	if err := provider.Validate(); err != nil {
		return nil, err
	}
	baseURL, _ := NormalizeBaseURL(provider.BaseURL)
	key, err := provider.APIKeyValue()
	if err != nil {
		return nil, err
	}
	if provider.Protocol == ProtocolAnthropic {
		return listAnthropicModels(ctx, baseURL, key)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	transport := providerTransport()
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request provider models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("provider models endpoint returned HTTP %d", response.StatusCode)
	}
	return decodeModels(response.Body)
}

func newProviderHTTPClient() *http.Client {
	transport := providerTransport()
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{Transport: transport, Timeout: 0, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func providerTransport() *http.Transport {
	if current, ok := http.DefaultTransport.(*http.Transport); ok {
		return current.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
}
