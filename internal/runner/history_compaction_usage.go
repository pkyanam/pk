package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkyanam/pk/internal/benchcontext"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

const (
	historyCompactionUsageVersion  = 1
	maxHistoryCompactionUsageBytes = 64 << 10
)

// HistoryCompactionUsage is a numeric-only ledger of summary-provider calls.
// Per-counter call counts make partial provider reporting explicit; nil totals
// mean that provider usage was unavailable for every summary attempt.
type HistoryCompactionUsage struct {
	Attempts              int    `json:"attempts"`
	Completed             int    `json:"completed"`
	Failed                int    `json:"failed"`
	UnknownUsageAttempts  int    `json:"unknown_usage_attempts"`
	InputTokens           *int64 `json:"input_tokens"`
	InputCalls            int    `json:"input_calls"`
	OutputTokens          *int64 `json:"output_tokens"`
	OutputCalls           int    `json:"output_calls"`
	CachedInputTokens     *int64 `json:"cached_input_tokens"`
	CachedInputCalls      int    `json:"cached_input_calls"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
	CacheWriteInputCalls  int    `json:"cache_write_input_calls"`
}

type historyCompactionUsageEnvelope struct {
	Version int                    `json:"version"`
	Record  HistoryCompactionUsage `json:"record"`
}

// HistoryCompactionUsageStore persists a content-free ledger separately from
// normal request usage and the summary checkpoint.
type HistoryCompactionUsageStore interface {
	LoadHistoryCompactionUsage(context.Context, string) (HistoryCompactionUsage, bool, error)
	RecordHistoryCompactionAttempt(context.Context, string, llm.Response, error) error
}

type fileHistoryCompactionUsageStore struct {
	directory string
	mu        sync.Mutex
}

// compactionUsageAdapter records each raw summary provider response before
// validation code can reject it. The wrapper never changes provider results
// and does not write into the latest ordinary-request usage record.
type compactionUsageAdapter struct {
	next    llm.Adapter
	store   HistoryCompactionUsageStore
	session string
	onError func(error)
}

func (adapter *compactionUsageAdapter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	if adapter == nil || adapter.next == nil {
		return llm.Response{}, errors.New("summary usage adapter is not configured")
	}
	response, callErr := adapter.next.Respond(ctx, request, options)
	if adapter.store != nil {
		persistCtx := context.WithoutCancel(ctx)
		if err := adapter.store.RecordHistoryCompactionAttempt(persistCtx, adapter.session, response, callErr); err != nil && adapter.onError != nil {
			adapter.onError(errors.New("save history compaction usage metrics"))
		}
	}
	return response, callErr
}

// NewLocalHistoryCompactionUsageStore creates a session-adjacent private store.
func NewLocalHistoryCompactionUsageStore(directory string) HistoryCompactionUsageStore {
	return &fileHistoryCompactionUsageStore{directory: directory}
}

// LoadHistoryCompactionUsage reads the ledger for one session. Missing sidecars
// are expected for sessions that have not used history summarization.
func LoadHistoryCompactionUsage(ctx context.Context, directory, sessionID string) (HistoryCompactionUsage, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return NewLocalHistoryCompactionUsageStore(directory).LoadHistoryCompactionUsage(ctx, sessionID)
}

func (store *fileHistoryCompactionUsageStore) path(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".context-compaction-usage.json")
}

func (store *fileHistoryCompactionUsageStore) LoadHistoryCompactionUsage(ctx context.Context, sessionID string) (HistoryCompactionUsage, bool, error) {
	if err := ctx.Err(); err != nil {
		return HistoryCompactionUsage{}, false, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return HistoryCompactionUsage{}, false, errors.New("session ID is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	path := store.path(sessionID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return HistoryCompactionUsage{}, false, nil
	}
	if err != nil {
		return HistoryCompactionUsage{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > maxHistoryCompactionUsageBytes {
		return HistoryCompactionUsage{}, false, errors.New("invalid or oversized history compaction usage metadata")
	}
	file, err := os.Open(path)
	if err != nil {
		return HistoryCompactionUsage{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxHistoryCompactionUsageBytes+1))
	if err != nil {
		return HistoryCompactionUsage{}, false, err
	}
	if len(data) > maxHistoryCompactionUsageBytes {
		return HistoryCompactionUsage{}, false, errors.New("history compaction usage metadata exceeds size limit")
	}
	var envelope historyCompactionUsageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != historyCompactionUsageVersion {
		return HistoryCompactionUsage{}, false, errors.New("invalid history compaction usage metadata")
	}
	if err := validateCompactionUsage(envelope.Record); err != nil {
		return HistoryCompactionUsage{}, false, err
	}
	return envelope.Record, true, nil
}

func (store *fileHistoryCompactionUsageStore) RecordHistoryCompactionAttempt(ctx context.Context, sessionID string, response llm.Response, callErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session ID is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.loadLocked(sessionID)
	if err != nil {
		return err
	}
	current.Attempts = satAddInt(current.Attempts, 1)
	if callErr == nil && response.Failure == nil && response.Stop == llm.StopComplete {
		current.Completed = satAddInt(current.Completed, 1)
	} else {
		current.Failed = satAddInt(current.Failed, 1)
	}
	usage := benchcontext.MeasureUsage(response.Usage)
	available := false
	if usage.InputAvailable && usage.InputTokens >= 0 {
		current.InputTokens = satAddPointer(current.InputTokens, usage.InputTokens)
		current.InputCalls = satAddInt(current.InputCalls, 1)
		available = true
	}
	if usage.OutputAvailable && usage.OutputTokens >= 0 {
		current.OutputTokens = satAddPointer(current.OutputTokens, usage.OutputTokens)
		current.OutputCalls = satAddInt(current.OutputCalls, 1)
		available = true
	}
	if usage.CachedInputAvailable && usage.CachedInputTokens >= 0 {
		current.CachedInputTokens = satAddPointer(current.CachedInputTokens, usage.CachedInputTokens)
		current.CachedInputCalls = satAddInt(current.CachedInputCalls, 1)
		available = true
	}
	if usage.CacheWriteAvailable && usage.CacheWriteTokens >= 0 {
		current.CacheWriteInputTokens = satAddPointer(current.CacheWriteInputTokens, usage.CacheWriteTokens)
		current.CacheWriteInputCalls = satAddInt(current.CacheWriteInputCalls, 1)
		available = true
	}
	if !available {
		current.UnknownUsageAttempts = satAddInt(current.UnknownUsageAttempts, 1)
	}
	return store.saveLocked(sessionID, current)
}

func (store *fileHistoryCompactionUsageStore) loadLocked(sessionID string) (HistoryCompactionUsage, error) {
	path := store.path(sessionID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return HistoryCompactionUsage{}, nil
	}
	if err != nil {
		return HistoryCompactionUsage{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > maxHistoryCompactionUsageBytes {
		return HistoryCompactionUsage{}, errors.New("invalid or oversized history compaction usage metadata")
	}
	file, err := os.Open(path)
	if err != nil {
		return HistoryCompactionUsage{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxHistoryCompactionUsageBytes+1))
	if err != nil {
		return HistoryCompactionUsage{}, err
	}
	if len(data) > maxHistoryCompactionUsageBytes {
		return HistoryCompactionUsage{}, errors.New("history compaction usage metadata exceeds size limit")
	}
	var envelope historyCompactionUsageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != historyCompactionUsageVersion {
		return HistoryCompactionUsage{}, errors.New("invalid history compaction usage metadata")
	}
	if err := validateCompactionUsage(envelope.Record); err != nil {
		return HistoryCompactionUsage{}, err
	}
	return envelope.Record, nil
}

func (store *fileHistoryCompactionUsageStore) saveLocked(sessionID string, record HistoryCompactionUsage) error {
	if err := validateCompactionUsage(record); err != nil {
		return err
	}
	if err := os.MkdirAll(store.directory, 0o700); err != nil {
		return fmt.Errorf("create compaction usage directory: %w", err)
	}
	data, err := json.Marshal(historyCompactionUsageEnvelope{Version: historyCompactionUsageVersion, Record: record})
	if err != nil {
		return err
	}
	if len(data) > maxHistoryCompactionUsageBytes {
		return errors.New("history compaction usage metadata exceeds size limit")
	}
	temp, err := os.CreateTemp(store.directory, ".context-compaction-usage-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), store.path(sessionID)); err != nil {
		return err
	}
	return nil
}

func validateCompactionUsage(record HistoryCompactionUsage) error {
	if record.Attempts < 0 || record.Completed < 0 || record.Failed < 0 || record.UnknownUsageAttempts < 0 || record.Failed > record.Attempts || record.Completed != record.Attempts-record.Failed || record.UnknownUsageAttempts > record.Attempts {
		return errors.New("invalid history compaction usage counters")
	}
	for _, counter := range []struct {
		value *int64
		calls int
	}{
		{record.InputTokens, record.InputCalls}, {record.OutputTokens, record.OutputCalls},
		{record.CachedInputTokens, record.CachedInputCalls}, {record.CacheWriteInputTokens, record.CacheWriteInputCalls},
	} {
		if counter.calls < 0 || counter.calls > record.Attempts || counter.value == nil && counter.calls != 0 || counter.value != nil && (*counter.value < 0 || counter.calls == 0) {
			return errors.New("invalid history compaction usage token counters")
		}
	}
	return nil
}

func satAddPointer(current *int64, increment int64) *int64 {
	if current == nil {
		current = new(int64)
	}
	if increment > 0 && *current > math.MaxInt64-increment {
		*current = math.MaxInt64
	} else if increment < 0 && *current < math.MinInt64-increment {
		*current = math.MinInt64
	} else {
		*current += increment
	}
	return current
}

func satAddInt(current, increment int) int {
	if increment > 0 && current > math.MaxInt-increment {
		return math.MaxInt
	}
	return current + increment
}
