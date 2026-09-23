//go:build pkbench

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/pkyanam/pk/cmd/pkbench/experiment"
	"github.com/pkyanam/pk/internal/benchcontext"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func beginBenchmarkRun(options *runner.Options) (func(), error) {
	policy := os.Getenv("PK_BENCH_POLICY")
	metricsPath := os.Getenv("PK_BENCH_CONTEXT_METRICS_FILE")
	if policy == "" && metricsPath == "" {
		return func() {}, nil
	}
	if policy == "" {
		policy = "current"
	}
	limit := 0
	compactSchema := false
	measureSchema := false
	compactReplay := false
	measureReplay := false
	replayThreshold := experiment.ReplayCompactionThreshold
	replayExcerpt := 768
	switch policy {
	case "current":
	case "output-cap-4k":
		limit = 4096
	case "compact-tool-schema":
		compactSchema = true
	case "tool-schema-current":
		measureSchema = true
	case "compact-replayed-shell-output":
		compactReplay = true
	case "compact-replayed-shell-output-aggressive":
		compactReplay = true
	case "compact-replayed-shell-output-large":
		compactReplay = true
	case "replay-compaction-current":
		measureReplay = true
	default:
		return nil, fmt.Errorf("unknown policy %q", policy)
	}
	if compactReplay || measureReplay {
		if raw := os.Getenv("PK_BENCH_REPLAY_THRESHOLD_BYTES"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 {
				return nil, fmt.Errorf("invalid replay threshold %q", raw)
			}
			replayThreshold = value
		}
		if raw := os.Getenv("PK_BENCH_REPLAY_EXCERPT_RUNES"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 {
				return nil, fmt.Errorf("invalid replay excerpt %q", raw)
			}
			replayExcerpt = value
		}
	}
	var metricsFile *os.File
	if metricsPath != "" {
		if options.Adapter == nil {
			return nil, fmt.Errorf("benchmark context metrics require an initialized adapter")
		}
		file, err := os.OpenFile(metricsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create benchmark context metrics file")
		}
		metricsFile = file
		options.Adapter = &benchcontext.Adapter{Next: options.Adapter, Output: metricsFile}
	}
	counters := &experiment.Counters{}
	replayCounters := &experiment.ReplayCompactionCounters{}
	var schemaMetrics experiment.ToolSchemaMetrics
	if path := os.Getenv("PK_BENCH_FAILED_COMMAND_LOG"); path != "" {
		counters.CaptureFailedCommands(path)
	}
	previousDecorator := options.DecorateRegistry
	options.DecorateRegistry = func(base tool.Registry) tool.Registry {
		if previousDecorator != nil {
			base = previousDecorator(base)
		}
		if compactSchema || measureSchema {
			if compactSchema {
				decorated, metrics := experiment.CompactToolDescriptions(base)
				schemaMetrics = metrics
				base = decorated
			} else {
				schemaMetrics = experiment.MeasureToolDescriptions(base)
			}
			return experiment.Decorate(base, 0, counters)
		}
		if compactReplay || measureReplay {
			base = experiment.Decorate(base, 0, counters)
			if compactReplay {
				return experiment.CompactCapturedShellResultsWithLimits(base, replayCounters, replayThreshold, replayExcerpt)
			}
			return experiment.MeasureCapturedShellResultsWithThreshold(base, replayCounters, replayThreshold)
		}
		return experiment.Decorate(base, limit, counters)
	}
	return func() {
		if metricsFile != nil {
			_ = metricsFile.Sync()
			_ = metricsFile.Close()
		}
		if !options.JSONL || options.Output == nil {
			return
		}
		if compactSchema || measureSchema {
			_ = json.NewEncoder(options.Output).Encode(map[string]any{
				"type": "benchmark_tool_schema", "mode": policy,
				"tool_description_bytes_before":   schemaMetrics.BeforeBytes,
				"tool_description_bytes_after":    schemaMetrics.AfterBytes,
				"tool_description_fields_changed": schemaMetrics.Changed,
				"tool_schema_metrics_available":   true,
			})
		}
		if compactReplay || measureReplay {
			_ = json.NewEncoder(options.Output).Encode(map[string]any{
				"type": "benchmark_context_compaction", "mode": policy,
				"threshold_bytes":                      replayThreshold,
				"excerpt_runes_each_side":              replayExcerpt,
				"context_compaction_metrics":           replayCounters.Snapshot(),
				"context_compaction_metrics_available": true,
			})
		}
		counts := counters.Snapshot()
		_ = json.NewEncoder(options.Output).Encode(map[string]any{
			"type": "benchmark_tool_limits", "mode": policy,
			"explicit_bash_output_limits":      counts.Explicit,
			"omitted_bash_output_limits":       counts.Omitted,
			"benchmark_default_applied":        counts.Defaulted,
			"bash_output_bytes":                counts.OutputBytes,
			"bash_error_bytes":                 counts.ErrorBytes,
			"bash_raw_output_bytes":            counts.RawOutputBytes,
			"bash_raw_error_bytes":             counts.RawErrorBytes,
			"bash_output_truncated_operations": counts.OutputTruncated,
			"bash_error_truncated_operations":  counts.ErrorTruncated,
			"bash_metrics_available":           true,
		})
	}, nil
}
