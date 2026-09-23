// Package modelstream adds bounded, safe Responses SSE progress observation to
// the pinned Unreal Responses adapter without changing its response parser.
package modelstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openaicodex"
	"github.com/unreallabsai/unreal-agent/harness/llm/responsesapi"
	"github.com/unreallabsai/unreal-agent/harness/primitives"
)

type Config struct {
	AccessToken string
	AccountID   string
	BaseURL     string
	MaxAttempts *int
}

// Event contains only safe UI progress: visible assistant text deltas and
// tool-call metadata/counts. It never includes prompts, reasoning, or tool
// argument contents.
type Event struct {
	RequestID string
	Attempt   int
	Status    int
	Kind      string
	Text      string
	ItemID    string
	ToolName  string
	Bytes     int
}

const (
	EventAttemptStarted        = "attempt_started"
	EventAttemptFailed         = "attempt_failed"
	EventRequestFailed         = "request_failed"
	EventResponseStarted       = "response_started"
	EventAssistantDelta        = "assistant_delta"
	EventToolCallStarted       = "tool_call_started"
	EventToolArgumentsProgress = "tool_arguments_progress"
	EventToolCallReady         = "tool_call_ready"
	EventResponseCompleted     = "response_completed"
	EventResponseIncomplete    = "response_incomplete"
	EventResponseFailed        = "response_failed"
)

type observerKey struct{}
type callKey struct{}

type observerConfig struct{ callback func(Event) }

type callState struct {
	requestID    string
	callback     func(Event)
	attempt      atomic.Int32
	deliveryMu   sync.Mutex
	mu           sync.Mutex
	lastEmit     time.Time
	flushTimer   *time.Timer
	flushVersion uint64
	pending      strings.Builder
	pendingID    string
	draftUsed    int
	pendingBytes int
	toolBytes    map[string]int
}

func WithObserver(ctx context.Context, callback func(Event)) context.Context {
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, observerKey{}, observerConfig{callback: callback})
}

type Client struct {
	adapter llm.Adapter
	remote  *primitives.RemoteClient
}

var _ llm.Adapter = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	if strings.TrimSpace(config.AccountID) == "" {
		return nil, errors.New("codex account ID must be set")
	}
	validator, err := openaicodex.NewClient(openaicodex.Config{
		AccessToken: config.AccessToken,
		AccountID:   config.AccountID,
		BaseURL:     config.BaseURL,
		MaxAttempts: config.MaxAttempts,
	})
	if err != nil {
		return nil, err
	}
	_ = validator.Close()
	token, account := strings.TrimSpace(config.AccessToken), strings.TrimSpace(config.AccountID)
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport is not an *http.Transport")
	}
	remote := primitives.NewRemoteClientWithHTTPClient(&http.Client{
		Transport:     &observingTransport{base: base.Clone()},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
	adapter, err := responsesapi.NewAdapter(remote, responsesapi.Config{
		Endpoint: codexBaseURL(config.BaseURL) + "/responses",
		Headers: map[string][]string{
			"Authorization":      {"Bearer " + token},
			"ChatGPT-Account-ID": {account},
			"Content-Type":       {"application/json"},
			"originator":         {"unreal-agent"},
			"User-Agent":         {"unreal-agent"},
		},
		CacheKeyPlacement: responsesapi.CacheKeyPlacement{UsePromptCacheKeyField: true, Header: "session-id"},
		MaxAttempts:       config.MaxAttempts,
	})
	if err != nil {
		_ = remote.Close()
		return nil, err
	}
	return &Client{adapter: adapter, remote: remote}, nil
}

func (client *Client) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	if request.Model.MaxOutputTokens != nil {
		return llm.Response{}, errors.New("codex does not support max_output_tokens")
	}
	if config, ok := ctx.Value(observerKey{}).(observerConfig); ok && config.callback != nil {
		id, err := requestID()
		if err != nil {
			return llm.Response{}, fmt.Errorf("create model request ID: %w", err)
		}
		ctx = context.WithValue(ctx, callKey{}, &callState{requestID: id, callback: config.callback})
	}
	response, err := client.adapter.Respond(ctx, request, options)
	if err != nil {
		if state, ok := ctx.Value(callKey{}).(*callState); ok {
			state.send(Event{Attempt: int(state.attempt.Load()), Kind: EventRequestFailed})
		}
	}
	return response, err
}

func (client *Client) Close() error { return client.remote.Close() }

func codexBaseURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return openaicodex.BaseURL
	}
	return value
}

func requestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
