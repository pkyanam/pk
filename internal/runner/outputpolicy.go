package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const (
	outputCompactionThreshold = 4 << 10
	outputCompactionRunes     = 768
	outputDecisionVersion     = 1
	maxOutputDecisionEntries  = 4096
	maxOutputDecisionBytes    = 16 << 10
	maxOutputDecisionStore    = 64 << 20
)

// OutputCompactionEvent reports one completed Bash result above the size
// threshold. Compacted is true only when all non-empty streams have readable
// capture files. The capture paths are emitted exactly as recorded by the
// operation; callers should treat them as local paths, not durable URLs.
type OutputCompactionEvent struct {
	OperationID    string   `json:"operation_id"`
	OriginalBytes  int      `json:"original_bytes"`
	StoredBytes    int      `json:"stored_bytes"`
	CapturePaths   []string `json:"capture_paths,omitempty"`
	ExitCode       int      `json:"exit_code"`
	Compacted      bool     `json:"compacted"`
	MissingCapture bool     `json:"missing_capture,omitempty"`
	Replayed       bool     `json:"replayed,omitempty"`
}

// OutputCompactionStore persists one-time translation decisions so session
// restore can reproduce the same tool-result prefix even when capture files
// are later removed. Implementations must make Save atomic and private.
type OutputCompactionStore interface {
	LoadOutputCompaction(operation.ID) (OutputCompactionEvent, string, bool, error)
	SaveOutputCompaction(operation.ID, OutputCompactionEvent, string) error
}

type outputCompactionDecision struct {
	event   OutputCompactionEvent
	summary string
}

type outputCompactionPolicy struct {
	callback  func(OutputCompactionEvent)
	store     OutputCompactionStore
	mu        sync.Mutex
	decisions map[string]outputCompactionDecision
	reported  map[string]struct{}
}

type outputCompactionRegistry struct {
	tool.Registry
	policy *outputCompactionPolicy
}

type outputCompactionTranslator struct {
	tool.Translator
	policy *outputCompactionPolicy
}

func compactCapturedOutputRegistry(base tool.Registry, callback func(OutputCompactionEvent), store OutputCompactionStore) tool.Registry {
	return outputCompactionRegistry{Registry: base, policy: &outputCompactionPolicy{callback: callback, store: store, decisions: make(map[string]outputCompactionDecision), reported: make(map[string]struct{})}}
}

func (r outputCompactionRegistry) Resolve(name string) (tool.Translator, bool) {
	t, ok := r.Registry.Resolve(name)
	if !ok || name != tool.BashName {
		return t, ok
	}
	return outputCompactionTranslator{Translator: t, policy: r.policy}, true
}

func (t outputCompactionTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	result, err := t.Translator.TranslateResult(callID, status, ops)
	if err != nil {
		return result, err
	}
	if status.Error != "" || len(ops) != 1 || ops[0].Type != operation.TypeShell || ops[0].Status != operation.StatusCompleted {
		return result, nil
	}
	state, err := operation.DecodeShellState(ops[0])
	if err != nil || state.Result == nil {
		return result, nil
	}
	if state.TerminalError != "" {
		return result, nil
	}
	for index := range result.Output {
		out := &result.Output[index]
		if out.Kind != llm.ToolResultText || len(out.Value) <= outputCompactionThreshold {
			continue
		}
		decision, fromStore, err := t.policy.decision(ops[0], state, out.Value)
		if err != nil {
			return llm.ToolResult{}, err
		}
		if decision.event.Compacted {
			out.Value = decision.summary
		}
		if t.policy.markReported(ops[0].ID) {
			event := cloneOutputCompactionEvent(decision.event)
			event.Replayed = fromStore
			if t.policy.callback != nil {
				t.policy.callback(event)
			}
		}
	}
	return result, nil
}

func (p *outputCompactionPolicy) decision(op operation.Operation, state operation.ShellState, original string) (outputCompactionDecision, bool, error) {
	id := op.ID
	p.mu.Lock()
	defer p.mu.Unlock()
	key := string(id)
	if decision, ok := p.decisions[key]; ok {
		return decision, false, nil
	}
	if p.store != nil {
		event, summary, found, err := p.store.LoadOutputCompaction(id)
		if err != nil {
			return outputCompactionDecision{}, false, fmt.Errorf("load saved Bash result summary for operation %s: %w", id, err)
		}
		if found {
			if event.OperationID != key || event.OriginalBytes != len(original) {
				return outputCompactionDecision{}, false, fmt.Errorf("saved Bash result summary for operation %s does not match the recorded operation", id)
			}
			if event.Compacted && (summary == "" || len(summary) != event.StoredBytes || len(event.CapturePaths) == 0) {
				return outputCompactionDecision{}, false, fmt.Errorf("saved Bash result summary for operation %s is invalid", id)
			}
			decision := outputCompactionDecision{event: cloneOutputCompactionEvent(event), summary: summary}
			p.decisions[key] = decision
			return decision, true, nil
		}
	}
	event := OutputCompactionEvent{OperationID: key, OriginalBytes: len(original), StoredBytes: len(original), ExitCode: state.Result.ExitCode}
	decision := outputCompactionDecision{event: event}
	paths, available := readableCapturePaths(state)
	if available {
		event.CapturePaths = paths
		event.Compacted = true
		decision.summary = compactOutputText(original, event)
		event.StoredBytes = len(decision.summary)
		decision.event = event
	} else {
		event.MissingCapture = true
		decision.event = event
	}
	if p.store != nil {
		if err := p.store.SaveOutputCompaction(id, cloneOutputCompactionEvent(event), decision.summary); err != nil {
			return outputCompactionDecision{}, false, fmt.Errorf("save Bash result summary for operation %s: %w", id, err)
		}
	}
	p.decisions[key] = decision
	return decision, false, nil
}

func (p *outputCompactionPolicy) markReported(id operation.ID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := string(id)
	if _, ok := p.reported[key]; ok {
		return false
	}
	p.reported[key] = struct{}{}
	return true
}

func cloneOutputCompactionEvent(event OutputCompactionEvent) OutputCompactionEvent {
	event.CapturePaths = append([]string(nil), event.CapturePaths...)
	return event
}

type localOutputCompactionStore struct{ directory string }
type outputDecisionRecord struct {
	Version int                   `json:"version"`
	Event   OutputCompactionEvent `json:"event"`
	Summary string                `json:"summary,omitempty"`
}

func newLocalOutputCompactionStore(directory string) OutputCompactionStore {
	return localOutputCompactionStore{directory: directory}
}
func (s localOutputCompactionStore) path(id operation.ID) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(s.directory, hex.EncodeToString(sum[:])+".json")
}
func (s localOutputCompactionStore) LoadOutputCompaction(id operation.ID) (OutputCompactionEvent, string, bool, error) {
	path := s.path(id)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return OutputCompactionEvent{}, "", false, nil
	}
	if err != nil {
		return OutputCompactionEvent{}, "", false, err
	}
	if len(data) > maxOutputDecisionBytes {
		return OutputCompactionEvent{}, "", false, fmt.Errorf("saved summary exceeds %d bytes", maxOutputDecisionBytes)
	}
	var record outputDecisionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return OutputCompactionEvent{}, "", false, err
	}
	if record.Version != outputDecisionVersion {
		return OutputCompactionEvent{}, "", false, fmt.Errorf("unsupported saved summary version %d", record.Version)
	}
	if record.Event.OperationID != string(id) || len(record.Summary) > outputCompactionThreshold*4 {
		return OutputCompactionEvent{}, "", false, fmt.Errorf("invalid saved summary record")
	}
	if record.Event.Compacted && record.Summary == "" {
		return OutputCompactionEvent{}, "", false, fmt.Errorf("compacted summary is empty")
	}
	return record.Event, record.Summary, true, nil
}
func (s localOutputCompactionStore) SaveOutputCompaction(id operation.ID, event OutputCompactionEvent, summary string) error {
	if event.OperationID != string(id) || len(summary) > outputCompactionThreshold*4 {
		return fmt.Errorf("invalid Bash result summary record")
	}
	if event.Compacted && (summary == "" || len(summary) != event.StoredBytes) {
		return fmt.Errorf("invalid compacted summary")
	}
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(s.directory)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("summary directory permissions %04o are not private", info.Mode().Perm())
	}
	files, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	total := int64(0)
	count := 0
	target := s.path(id)
	for _, entry := range files {
		if entry.IsDir() {
			continue
		}
		entryInfo, e := entry.Info()
		if e != nil {
			return e
		}
		total += entryInfo.Size()
		count++
		if filepath.Join(s.directory, entry.Name()) == target {
			count--
			total -= entryInfo.Size()
		}
	}
	if count >= maxOutputDecisionEntries {
		return fmt.Errorf("summary store entry limit (%d) reached", maxOutputDecisionEntries)
	}
	if total+int64(len(summary))+1024 > maxOutputDecisionStore {
		return fmt.Errorf("summary store byte limit (%d) reached", maxOutputDecisionStore)
	}
	record := outputDecisionRecord{Version: outputDecisionVersion, Event: event, Summary: summary}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxOutputDecisionBytes {
		return fmt.Errorf("summary record exceeds %d bytes", maxOutputDecisionBytes)
	}
	temp, err := os.CreateTemp(s.directory, ".summary-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempName, target); err != nil {
		return err
	}
	dir, err := os.Open(s.directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readableCapturePaths(state operation.ShellState) ([]string, bool) {
	if state.Result == nil {
		return nil, false
	}
	streams := []struct{ label, path, content string }{{"stdout", state.OutPath, state.Result.Out}, {"stderr", state.ErrPath, state.Result.Err}}
	paths := make([]string, 0, 2)
	for _, stream := range streams {
		if stream.content == "" {
			continue
		}
		path := stream.path
		if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
			return nil, false
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, false
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		paths = append(paths, stream.label+": "+path)
	}
	if len(paths) == 0 {
		return nil, false
	}
	return paths, true
}

func compactOutputText(input string, decision OutputCompactionEvent) string {
	left, right := firstOutputRunes(input, outputCompactionRunes), lastOutputRunes(input, outputCompactionRunes)
	omitted := len([]rune(input)) - len([]rune(left)) - len([]rune(right))
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n[Earlier Bash result compacted for context: %d characters omitted. Complete captures: %s. Read the capture file(s) if more detail is needed. Exit code: %d.", left, omitted, strings.Join(decision.CapturePaths, "; "), decision.ExitCode)
	fmt.Fprintf(&b, "]\n%s", right)
	return b.String()
}

func firstOutputRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
func lastOutputRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[len(r)-n:]
	}
	return string(r)
}

var _ tool.Registry = outputCompactionRegistry{}
var _ tool.Translator = outputCompactionTranslator{}
