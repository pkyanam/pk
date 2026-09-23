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

// Event contains transient UI progress: visible assistant text, tool metadata,
// and (only when explicitly opted in) external-provider reasoning deltas. It
// never includes prompts or tool argument contents.
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
	EventAttemptStarted         = "attempt_started"
	EventAttemptFailed          = "attempt_failed"
	EventRequestFailed          = "request_failed"
	EventResponseStarted        = "response_started"
	EventAssistantDelta         = "assistant_delta"
	EventReasoningProgress      = "reasoning_progress"
	EventProviderReasoningDelta = "provider_reasoning_delta"
	EventToolCallStarted        = "tool_call_started"
	EventToolArgumentsProgress  = "tool_arguments_progress"
	EventToolCallReady          = "tool_call_ready"
	EventResponseCompleted      = "response_completed"
	EventResponseIncomplete     = "response_incomplete"
	EventResponseFailed         = "response_failed"
)

type observerKey struct{}
type callKey struct{}
type providerReasoningKey struct{}

const maxProviderReasoningText = 32 << 10

// WithProviderReasoning opts this call context into receiving transient text
// explicitly returned in an external provider's reasoning fields. The text is
// never added to llm.Response or the session history by modelstream.
func WithProviderReasoning(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, providerReasoningKey{}, enabled)
}

// ProviderReasoningEnabled reports whether external provider reasoning deltas
// may be forwarded in this context. Internal/suppressed calls always return
// false.
func ProviderReasoningEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(providerReasoningKey{}).(bool)
	config, _ := ctx.Value(observerKey{}).(observerConfig)
	return enabled && !config.suppressed
}

type observerConfig struct {
	callback   func(Event)
	onOutput   func()
	suppressed bool
}

// ObservedCall adapts streaming from providers that do not use the Responses
// transport observer to the same bounded progress event path.
type ObservedCall struct {
	state             *callState
	providerReasoning bool
	once              sync.Once
}

// BeginObservedCall starts an observed provider call. requestID may be empty,
// in which case a random ID is generated. Suppressed or unobserved calls still
// return a call handle so TrackOutput can observe visible output safely.
func BeginObservedCall(ctx context.Context, id string, attempt int) (*ObservedCall, error) {
	config, _ := ctx.Value(observerKey{}).(observerConfig)
	if id == "" {
		var err error
		id, err = requestID()
		if err != nil {
			return nil, fmt.Errorf("create model request ID: %w", err)
		}
	}
	if attempt < 1 {
		attempt = 1
	}
	state := &callState{requestID: id, callback: config.callback, onOutput: config.onOutput}
	state.attempt.Store(int32(attempt))
	call := &ObservedCall{state: state, providerReasoning: ProviderReasoningEnabled(ctx)}
	state.emit(Event{Attempt: attempt, Kind: EventAttemptStarted})
	return call, nil
}

// Emit sends a provider progress event through the shared coalescing and
// output-tracking path. Request and attempt identifiers are set by the call.
func (call *ObservedCall) Emit(event Event) {
	if call == nil || call.state == nil {
		return
	}
	if event.Kind == EventProviderReasoningDelta && !call.providerReasoning {
		return
	}
	if event.Attempt == 0 {
		event.Attempt = int(call.state.attempt.Load())
	}
	call.state.emit(event)
}

// Finish flushes pending progress before a terminal event. It is safe to call
// more than once; only the first result is emitted.
func (call *ObservedCall) Finish(err error) {
	if call == nil || call.state == nil {
		return
	}
	call.once.Do(func() {
		attempt := int(call.state.attempt.Load())
		if err != nil {
			call.state.emit(Event{Attempt: attempt, Kind: EventRequestFailed})
			return
		}
		call.state.flush(Event{Attempt: attempt, Kind: EventResponseCompleted})
	})
}

type callState struct {
	requestID                string
	callback                 func(Event)
	onOutput                 func()
	attempt                  atomic.Int32
	deliveryMu               sync.Mutex
	mu                       sync.Mutex
	lastEmit                 time.Time
	flushTimer               *time.Timer
	flushVersion             uint64
	pending                  strings.Builder
	pendingID                string
	draftUsed                int
	pendingBytes             int
	pendingReasoningBytes    int
	pendingProviderReasoning strings.Builder
	providerReasoningUsed    int
	toolBytes                map[string]int
}

func WithObserver(ctx context.Context, callback func(Event)) context.Context {
	if callback == nil {
		return ctx
	}
	config, _ := ctx.Value(observerKey{}).(observerConfig)
	if config.suppressed {
		return ctx
	}
	config.callback = callback
	return context.WithValue(ctx, observerKey{}, config)
}

// WithoutObserver prevents internal model work, such as context summaries, from
// appearing as assistant text in the user's conversation. Cancellation and other
// context values are preserved.
func WithoutObserver(ctx context.Context) context.Context {
	return context.WithValue(ctx, observerKey{}, observerConfig{suppressed: true})
}

// TrackOutput observes output before UI coalescing, including deltas discarded
// after a failed stream. The returned predicate is safe to call concurrently.
// A caller deciding whether to retry must also inspect the returned response;
// this tracker only covers providers using this streaming observer.
func TrackOutput(ctx context.Context) (context.Context, func() bool) {
	config, _ := ctx.Value(observerKey{}).(observerConfig)
	var produced atomic.Bool
	previous := config.onOutput
	config.onOutput = func() {
		produced.Store(true)
		if previous != nil {
			previous()
		}
	}
	if config.callback == nil {
		config.callback = func(Event) {}
	}
	return context.WithValue(ctx, observerKey{}, config), produced.Load
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
		ctx = context.WithValue(ctx, callKey{}, &callState{requestID: id, callback: config.callback, onOutput: config.onOutput})
	}
	response, err := client.adapter.Respond(ctx, request, options)
	if err != nil {
		if state, ok := ctx.Value(callKey{}).(*callState); ok {
			state.emit(Event{Attempt: int(state.attempt.Load()), Kind: EventRequestFailed})
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
