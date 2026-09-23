//go:build pkbench

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pkyanam/pk/cmd/pkbench/experiment"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func beginBenchmarkRun(options *runner.Options) (func(), error) {
	policy := os.Getenv("PK_BENCH_POLICY")
	if policy == "" {
		return func() {}, nil
	}
	limit := 0
	compactSchema := false
	measureSchema := false
	compactReplay := false
	measureReplay := false
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
	case "replay-compaction-current":
		measureReplay = true
	default:
		return nil, fmt.Errorf("unknown policy %q", policy)
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
				return experiment.CompactCapturedShellResults(base, replayCounters)
			}
			return experiment.MeasureCapturedShellResults(base, replayCounters)
		}
		return experiment.Decorate(base, limit, counters)
	}
	return func() {
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
				"threshold_bytes":                      experiment.ReplayCompactionThreshold,
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
