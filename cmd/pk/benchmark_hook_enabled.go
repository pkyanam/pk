//go:build pkbench

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/pkyanam/pk/cmd/pkbench/experiment"
	"github.com/pkyanam/pk/internal/benchcontext"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
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
	compactSubagentSchema := false
	measureSubagentSchema := false
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
	case "compact-subagent-schema":
		compactSubagentSchema = true
	case "subagent-schema-current":
		measureSubagentSchema = true
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
	subagentSchemaMetrics := &subagentSchemaMetrics{}
	var schemaMetrics experiment.ToolSchemaMetrics
	if path := os.Getenv("PK_BENCH_FAILED_COMMAND_LOG"); path != "" {
		counters.CaptureFailedCommands(path)
	}
	previousDecorator := options.DecorateRegistry
	options.DecorateRegistry = func(base tool.Registry) tool.Registry {
		if previousDecorator != nil {
			base = previousDecorator(base)
		}
		if compactSubagentSchema || measureSubagentSchema {
			if compactSubagentSchema {
				decorated, metrics, err := experiment.CompactSubagentTools(base)
				if err != nil {
					subagentSchemaMetrics.setError(err.Error())
					return experiment.Decorate(base, 0, counters)
				}
				subagentSchemaMetrics.set(metrics.BeforeBytes, metrics.AfterBytes)
				base = decorated
			} else {
				before, err := serializedSubagentDefinitionBytes(base.StaticDefinitions())
				if err != nil {
					subagentSchemaMetrics.setError(err.Error())
					return experiment.Decorate(base, 0, counters)
				}
				subagentSchemaMetrics.set(before, before)
			}
			return experiment.Decorate(base, 0, counters)
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
	if compactSubagentSchema || measureSubagentSchema {
		if options.Adapter == nil {
			return nil, fmt.Errorf("subagent schema experiment requires an initialized adapter")
		}
		options.Adapter = &subagentSchemaAdapter{Next: options.Adapter, state: subagentSchemaMetrics, compact: compactSubagentSchema}
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
		if compactSubagentSchema || measureSubagentSchema {
			before, after, errText := subagentSchemaMetrics.snapshot()
			payload := map[string]any{
				"type": "benchmark_subagent_schema", "mode": policy,
				"subagent_tool_definition_bytes_before": before,
				"subagent_tool_definition_bytes_after":  after,
				"subagent_tool_definition_bytes_saved":  before - after,
				"subagent_schema_metrics_available":     errText == "" && before > 0,
			}
			if errText != "" {
				payload["error"] = errText
			}
			_ = json.NewEncoder(options.Output).Encode(payload)
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

type subagentSchemaMetrics struct {
	mu     sync.Mutex
	before int64
	after  int64
	err    string
	ready  bool
}

func (m *subagentSchemaMetrics) set(before, after int64) {
	m.mu.Lock()
	m.before, m.after, m.ready = before, after, true
	m.mu.Unlock()
}

func (m *subagentSchemaMetrics) setError(message string) {
	m.mu.Lock()
	m.err = message
	m.mu.Unlock()
}

func (m *subagentSchemaMetrics) snapshot() (int64, int64, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ready && m.err == "" {
		return 0, 0, "registry decoration did not run"
	}
	return m.before, m.after, m.err
}

type subagentSchemaAdapter struct {
	Next    llm.Adapter
	state   *subagentSchemaMetrics
	compact bool
}

func (a *subagentSchemaAdapter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	var bytes int64
	var current, compact int
	var names = map[string]bool{}
	for _, definition := range request.Tools {
		switch definition.Name {
		case "SubagentStart", "SubagentStatus", "SubagentSend", "SubagentWait", "SubagentCancel":
			if names[definition.Name] {
				return llm.Response{}, fmt.Errorf("subagent schema experiment observed duplicate tool %q", definition.Name)
			}
			names[definition.Name] = true
			current++
		case "Subagent":
			compact++
		default:
			continue
		}
		encoded, err := json.Marshal(definition)
		if err != nil {
			return llm.Response{}, fmt.Errorf("measure subagent request schema")
		}
		bytes += int64(len(encoded))
	}
	if a.compact {
		if current != 0 || compact != 1 {
			return llm.Response{}, fmt.Errorf("compact subagent schema arm expected one Subagent definition, got %d legacy and %d compact", current, compact)
		}
	} else if current != 5 || compact != 0 {
		return llm.Response{}, fmt.Errorf("current subagent schema arm expected five legacy definitions, got %d legacy and %d compact", current, compact)
	}
	a.state.mu.Lock()
	if a.state.err != "" {
		errText := a.state.err
		a.state.mu.Unlock()
		return llm.Response{}, fmt.Errorf("subagent schema experiment registry setup failed: %s", errText)
	}
	if !a.state.ready {
		a.state.err = "registry decoration did not run"
		a.state.mu.Unlock()
		return llm.Response{}, fmt.Errorf("subagent schema experiment registry decoration did not run")
	}
	want := a.state.after
	if bytes != want {
		a.state.err = fmt.Sprintf("request schema bytes %d do not match decorated registry bytes %d", bytes, want)
		a.state.mu.Unlock()
		return llm.Response{}, fmt.Errorf("subagent schema experiment request differs from measured registry")
	}
	a.state.mu.Unlock()
	return a.Next.Respond(ctx, request, options)
}

func serializedSubagentDefinitionBytes(definitions []tool.Definition) (int64, error) {
	var total int64
	count := 0
	for _, definition := range definitions {
		switch definition.Tool.Name {
		case "SubagentStart", "SubagentStatus", "SubagentSend", "SubagentWait", "SubagentCancel":
			encoded, err := json.Marshal(definition.Tool)
			if err != nil {
				return 0, fmt.Errorf("measure current subagent schema")
			}
			total += int64(len(encoded))
			count++
		}
	}
	if count != 5 {
		return 0, fmt.Errorf("current subagent schema expected five definitions, found %d", count)
	}
	return total, nil
}
