package experiment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// ReplayCompactionThreshold only affects completed Bash results that are large
// enough to become expensive when replayed in later model requests.
const ReplayCompactionThreshold = 4_096

const replayExcerptRunes = 768

type ReplayCompactionCounts struct {
	EligibleResults  int64 `json:"eligible_bash_results"`
	CompactedResults int64 `json:"compacted_bash_results"`
	OriginalBytes    int64 `json:"original_result_bytes"`
	StoredBytes      int64 `json:"stored_result_bytes"`
	MissingCapture   int64 `json:"missing_capture_fallbacks"`
}

type ReplayCompactionCounters struct {
	eligible  atomic.Int64
	compacted atomic.Int64
	original  atomic.Int64
	stored    atomic.Int64
	missing   atomic.Int64
}

func (c *ReplayCompactionCounters) Snapshot() ReplayCompactionCounts {
	return ReplayCompactionCounts{c.eligible.Load(), c.compacted.Load(), c.original.Load(), c.stored.Load(), c.missing.Load()}
}

// CompactCapturedShellResults wraps only Bash result translation. It replaces
// large result text with a bounded excerpt and the operation's exact capture
// paths. The replacement is made once as the tool result enters the durable
// conversation, so the resulting context prefix stays stable across turns.
func CompactCapturedShellResults(base tool.Registry, counters *ReplayCompactionCounters) tool.Registry {
	if counters == nil {
		counters = &ReplayCompactionCounters{}
	}
	return replayCompactionRegistry{Registry: base, counters: counters, compact: true}
}

func MeasureCapturedShellResults(base tool.Registry, counters *ReplayCompactionCounters) tool.Registry {
	if counters == nil {
		counters = &ReplayCompactionCounters{}
	}
	return replayCompactionRegistry{Registry: base, counters: counters}
}

type replayCompactionRegistry struct {
	tool.Registry
	counters *ReplayCompactionCounters
	compact  bool
}

func (r replayCompactionRegistry) Resolve(name string) (tool.Translator, bool) {
	translator, ok := r.Registry.Resolve(name)
	if !ok || name != tool.BashName {
		return translator, ok
	}
	return replayCompactionTranslator{Translator: translator, counters: r.counters, compact: r.compact}, true
}

type replayCompactionTranslator struct {
	tool.Translator
	counters *ReplayCompactionCounters
	compact  bool
}

func (t replayCompactionTranslator) TranslateResult(callID string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	result, err := t.Translator.TranslateResult(callID, status, operations)
	if err != nil {
		return result, err
	}
	if len(operations) != 1 || operations[0].Status != operation.StatusCompleted {
		return result, nil
	}
	state, err := operation.DecodeShellState(operations[0])
	if err != nil || state.Result == nil {
		return result, nil
	}
	for i := range result.Output {
		if result.Output[i].Kind != llm.ToolResultText {
			continue
		}
		original := result.Output[i].Value
		if len(original) <= ReplayCompactionThreshold {
			continue
		}
		t.counters.eligible.Add(1)
		t.counters.original.Add(int64(len(original)))
		compacted, capturesExist := compactResultWithCaptures(original, state)
		if !capturesExist {
			t.counters.missing.Add(1)
			t.counters.stored.Add(int64(len(original)))
			continue
		}
		if t.compact {
			result.Output[i].Value = compacted
			t.counters.compacted.Add(1)
			t.counters.stored.Add(int64(len(compacted)))
		} else {
			t.counters.stored.Add(int64(len(original)))
		}
	}
	return result, nil
}

func compactResultWithCaptures(input string, state operation.ShellState) (string, bool) {
	paths := make([]string, 0, 2)
	for index, path := range []string{state.OutPath, state.ErrPath} {
		if path == "" || !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			label := "stdout"
			if index == 1 {
				label = "stderr"
			}
			paths = append(paths, label+": "+path)
		}
	}
	if len(paths) == 0 {
		return input, false
	}
	left, right := firstRunes(input, replayExcerptRunes), lastRunes(input, replayExcerptRunes)
	omitted := len([]rune(input)) - len([]rune(left)) - len([]rune(right))
	return fmt.Sprintf("%s\n[Earlier Bash result compacted for context: %d characters omitted. Complete captures: %s. Read the capture file(s) if the excerpt is insufficient. Exit code: %d.]\n%s", left, omitted, strings.Join(paths, "; "), state.Result.ExitCode, right), true
}

func firstRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		runes = runes[:n]
	}
	return string(runes)
}

func lastRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		runes = runes[len(runes)-n:]
	}
	return string(runes)
}

var _ tool.Registry = replayCompactionRegistry{}
var _ tool.Translator = replayCompactionTranslator{}
