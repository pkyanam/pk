package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkyanam/pk/internal/benchcontext"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

const contextUsageVersion = 1
const maxContextUsageBytes = 64 << 10

// ContextUsageCategory describes one disjoint portion of the latest provider
// request. Bytes are JSON-encoded values from the harness request, not tokens
// and not the complete serialized provider wire payload.
type ContextUsageCategory struct {
	ID    string `json:"id"`
	Items int    `json:"items"`
	Bytes int64  `json:"bytes"`
}

// ContextProviderUsage preserves unknown counters as JSON null instead of
// turning missing provider fields into zero.
type ContextProviderUsage struct {
	InputTokens         *int64 `json:"input_tokens"`
	InputAvailable      bool   `json:"input_tokens_available"`
	OutputTokens        *int64 `json:"output_tokens"`
	OutputAvailable     bool   `json:"output_tokens_available"`
	CachedInputTokens   *int64 `json:"cached_input_tokens"`
	CachedAvailable     bool   `json:"cached_input_tokens_available"`
	CacheWriteTokens    *int64 `json:"cache_write_input_tokens"`
	CacheWriteAvailable bool   `json:"cache_write_input_tokens_available"`
}

// ContextUsageRecord is a content-free measurement of the latest provider
// request and its matching response. Provider counters remain separate from
// byte composition; no tokenizer or context-window estimate is applied.
type ContextUsageRecord struct {
	Available           bool                   `json:"available"`
	Pending             bool                   `json:"pending"`
	ResponseFailed      bool                   `json:"response_failed,omitempty"`
	RequestOrdinal      int                    `json:"request_ordinal,omitempty"`
	Measurement         string                 `json:"measurement,omitempty"`
	Categories          []ContextUsageCategory `json:"categories,omitempty"`
	TotalBytes          int64                  `json:"total_bytes,omitempty"`
	LatestProviderUsage ContextProviderUsage   `json:"latest_provider_usage"`
	ContextLimitTokens  *int64                 `json:"context_limit_tokens"`
}

type contextUsageEnvelope struct {
	Version int                `json:"version"`
	Record  ContextUsageRecord `json:"record"`
}

// ContextUsageStore persists only bounded numeric context measurements. It
// gives hosts with custom session storage a way to keep usage metadata beside
// their saved sessions without changing the upstream session log format.
type ContextUsageStore interface {
	LoadContextUsage(context.Context, session.ID) (ContextUsageRecord, error)
	SaveContextUsage(context.Context, session.ID, ContextUsageRecord) error
}

type fileContextUsageStore struct{ directory string }

func (store fileContextUsageStore) path(id session.ID) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".context-usage.json")
}

func (store fileContextUsageStore) LoadContextUsage(ctx context.Context, id session.ID) (ContextUsageRecord, error) {
	if err := ctx.Err(); err != nil {
		return ContextUsageRecord{}, err
	}
	file, err := os.Open(store.path(id))
	if err != nil {
		return ContextUsageRecord{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxContextUsageBytes+1))
	if err != nil {
		return ContextUsageRecord{}, err
	}
	if len(data) > maxContextUsageBytes {
		return ContextUsageRecord{}, errors.New("context usage metadata exceeds size limit")
	}
	var envelope contextUsageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ContextUsageRecord{}, fmt.Errorf("decode context usage metadata: %w", err)
	}
	if envelope.Version != contextUsageVersion {
		return ContextUsageRecord{}, fmt.Errorf("unsupported context usage metadata version %d", envelope.Version)
	}
	return envelope.Record, nil
}

func (store fileContextUsageStore) SaveContextUsage(ctx context.Context, id session.ID, record ContextUsageRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(store.directory, 0o700); err != nil {
		return fmt.Errorf("create context usage directory: %w", err)
	}
	data, err := json.Marshal(contextUsageEnvelope{Version: contextUsageVersion, Record: record})
	if err != nil {
		return fmt.Errorf("encode context usage metadata: %w", err)
	}
	if len(data) > maxContextUsageBytes {
		return errors.New("context usage metadata exceeds size limit")
	}
	temp, err := os.CreateTemp(store.directory, ".context-usage-*.tmp")
	if err != nil {
		return fmt.Errorf("create context usage metadata: %w", err)
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure context usage metadata: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write context usage metadata: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close context usage metadata: %w", err)
	}
	if err := os.Rename(temp.Name(), store.path(id)); err != nil {
		return fmt.Errorf("publish context usage metadata: %w", err)
	}
	return nil
}

func defaultContextUsageStore(sessionDir, workspace string) ContextUsageStore {
	directory := strings.TrimSpace(sessionDir)
	if directory == "" {
		directory = filepath.Join(workspace, ".pk", "contexts")
	}
	return fileContextUsageStore{directory: directory}
}

// LoadContextUsage reads the latest saved context measurement. Missing
// metadata is expected for legacy sessions and returns (zero, false, nil).
func LoadContextUsage(ctx context.Context, sessionDir, workspace, sessionID string) (ContextUsageRecord, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(sessionID) == "" {
		return ContextUsageRecord{}, false, nil
	}
	record, err := defaultContextUsageStore(sessionDir, workspace).LoadContextUsage(ctx, session.ID(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return ContextUsageRecord{}, false, nil
	}
	if err != nil {
		return ContextUsageRecord{}, false, err
	}
	return record, true, nil
}

type contextUsageAdapter struct {
	next    llm.Adapter
	store   ContextUsageStore
	id      session.ID
	mu      sync.Mutex
	ordinal int
	onError func(error)
}

func (adapter *contextUsageAdapter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	record, err := benchcontext.MeasureRequest(request)
	if err != nil {
		// Instrumentation must not prevent the provider call. Report only the
		// bounded generic error; never include request content.
		if adapter.onError != nil {
			adapter.onError(errors.New("measure provider request context"))
		}
		return adapter.next.Respond(ctx, request, options)
	}
	adapter.mu.Lock()
	adapter.ordinal++
	ordinal := adapter.ordinal
	adapter.mu.Unlock()
	pending := contextUsageRecord(record, ordinal, benchcontext.Usage{}, true)
	adapter.persist(ctx, pending)
	response, responseErr := adapter.next.Respond(ctx, request, options)
	completed := contextUsageRecord(record, ordinal, benchcontext.MeasureUsage(response.Usage), false)
	completed.ResponseFailed = responseErr != nil
	// The coordinator may stop without joining an in-flight adapter request. Do
	// not let a late canceled response overwrite usage from a later turn.
	if ctx.Err() == nil {
		adapter.persist(ctx, completed)
	}
	return response, responseErr
}

func (adapter *contextUsageAdapter) persist(ctx context.Context, record ContextUsageRecord) {
	if adapter.store == nil {
		return
	}
	if err := adapter.store.SaveContextUsage(ctx, adapter.id, record); err != nil && adapter.onError != nil {
		adapter.onError(errors.New("save provider context metrics"))
	}
}

func contextUsageRecord(measured benchcontext.Record, ordinal int, usage benchcontext.Usage, pending bool) ContextUsageRecord {
	categories := make([]ContextUsageCategory, 0, 6)
	appendSize := func(id string, size benchcontext.Size) {
		categories = append(categories, ContextUsageCategory{ID: id, Items: size.Items, Bytes: size.Bytes})
	}
	appendSize("system_prompt", measured.SystemPrompt)
	appendSize("tool_schemas", measured.ToolSchemas)
	var messages benchcontext.Size
	for role, size := range measured.MessageRoles {
		if role == "system" {
			continue
		}
		messages.Items += size.Items
		messages.Bytes += size.Bytes
	}
	appendSize("messages", messages)
	appendSize("tool_calls", measured.ToolCalls)
	appendSize("tool_results", measured.ToolResults)
	appendSize("other_input", measured.OtherInput)
	return ContextUsageRecord{
		Available: true, Pending: pending, RequestOrdinal: ordinal,
		Measurement: "json_value_bytes", Categories: categories,
		TotalBytes:          measured.InputValueBytes + measured.ToolSchemas.Bytes,
		LatestProviderUsage: contextProviderUsage(usage),
		ContextLimitTokens:  nil,
	}
}

func contextProviderUsage(usage benchcontext.Usage) ContextProviderUsage {
	result := ContextProviderUsage{
		InputAvailable: usage.InputAvailable, OutputAvailable: usage.OutputAvailable,
		CachedAvailable: usage.CachedInputAvailable, CacheWriteAvailable: usage.CacheWriteAvailable,
	}
	if usage.InputAvailable {
		result.InputTokens = contextUsageInt64Pointer(usage.InputTokens)
	}
	if usage.OutputAvailable {
		result.OutputTokens = contextUsageInt64Pointer(usage.OutputTokens)
	}
	if usage.CachedInputAvailable {
		result.CachedInputTokens = contextUsageInt64Pointer(usage.CachedInputTokens)
	}
	if usage.CacheWriteAvailable {
		result.CacheWriteTokens = contextUsageInt64Pointer(usage.CacheWriteTokens)
	}
	return result
}

func contextUsageInt64Pointer(value int64) *int64 { return &value }
