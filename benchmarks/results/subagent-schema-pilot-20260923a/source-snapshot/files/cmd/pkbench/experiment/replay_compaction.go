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
	return CompactCapturedShellResultsWithLimits(base, counters, ReplayCompactionThreshold, replayExcerptRunes)
}

// CompactCapturedShellResultsWithLimits is used by opt-in benchmark treatments;
// production runner defaults do not call it.
func CompactCapturedShellResultsWithLimits(base tool.Registry, counters *ReplayCompactionCounters, thresholdBytes, excerptRunes int) tool.Registry {
	if counters == nil {
		counters = &ReplayCompactionCounters{}
	}
	if thresholdBytes < 1 {
		thresholdBytes = ReplayCompactionThreshold
	}
	if excerptRunes < 1 {
		excerptRunes = replayExcerptRunes
	}
	return replayCompactionRegistry{Registry: base, counters: counters, compact: true, thresholdBytes: thresholdBytes, excerptRunes: excerptRunes}
}

func MeasureCapturedShellResults(base tool.Registry, counters *ReplayCompactionCounters) tool.Registry {
	return MeasureCapturedShellResultsWithThreshold(base, counters, ReplayCompactionThreshold)
}

func MeasureCapturedShellResultsWithThreshold(base tool.Registry, counters *ReplayCompactionCounters, thresholdBytes int) tool.Registry {
	if counters == nil {
		counters = &ReplayCompactionCounters{}
	}
	if thresholdBytes < 1 {
		thresholdBytes = ReplayCompactionThreshold
	}
	return replayCompactionRegistry{Registry: base, counters: counters, thresholdBytes: thresholdBytes, excerptRunes: replayExcerptRunes}
}

type replayCompactionRegistry struct {
	tool.Registry
	counters       *ReplayCompactionCounters
	compact        bool
	thresholdBytes int
	excerptRunes   int
}

func (r replayCompactionRegistry) Resolve(name string) (tool.Translator, bool) {
	translator, ok := r.Registry.Resolve(name)
	if !ok || name != tool.BashName {
		return translator, ok
	}
	return replayCompactionTranslator{Translator: translator, counters: r.counters, compact: r.compact, thresholdBytes: r.thresholdBytes, excerptRunes: r.excerptRunes}, true
}

type replayCompactionTranslator struct {
	tool.Translator
	counters       *ReplayCompactionCounters
	compact        bool
	thresholdBytes int
	excerptRunes   int
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
		if len(original) <= t.thresholdBytes {
			continue
		}
		t.counters.eligible.Add(1)
		t.counters.original.Add(int64(len(original)))
		compacted, capturesExist := compactResultWithCapturesAndExcerpt(original, state, t.excerptRunes)
		if !capturesExist {
			t.counters.missing.Add(1)
			t.counters.stored.Add(int64(len(original)))
			continue
		}
		if t.compact {
			if len(compacted) >= len(original) {
				t.counters.stored.Add(int64(len(original)))
				continue
			}
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
	return compactResultWithCapturesAndExcerpt(input, state, replayExcerptRunes)
}

func compactResultWithCapturesAndExcerpt(input string, state operation.ShellState, excerptRunes int) (string, bool) {
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
	if excerptRunes < 1 {
		excerptRunes = replayExcerptRunes
	}
	runes := []rune(input)
	headCount := excerptRunes
	if headCount > len(runes) {
		headCount = len(runes)
	}
	tailStart := len(runes) - excerptRunes
	if tailStart < headCount {
		tailStart = headCount
	}
	left, right := string(runes[:headCount]), string(runes[tailStart:])
	omitted := len(runes) - headCount - len(runes[tailStart:])
	return fmt.Sprintf("%s\n[Earlier Bash result compacted for context: %d characters omitted. Complete captures: %s. Read the capture file(s) if the excerpt is insufficient. Exit code: %d.]\n%s", left, omitted, strings.Join(paths, "; "), state.Result.ExitCode, right), true
}

var _ tool.Registry = replayCompactionRegistry{}
var _ tool.Translator = replayCompactionTranslator{}
