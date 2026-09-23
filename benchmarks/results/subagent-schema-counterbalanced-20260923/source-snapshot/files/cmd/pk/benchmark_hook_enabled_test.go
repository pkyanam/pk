//go:build pkbench

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/helperregistry"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestReplayOverrideEnvironmentIsScopedToReplayPolicies(t *testing.T) {
	t.Setenv("PK_BENCH_REPLAY_THRESHOLD_BYTES", "invalid")
	t.Setenv("PK_BENCH_REPLAY_EXCERPT_RUNES", "invalid")
	t.Setenv("PK_BENCH_POLICY", "current")
	if _, err := beginBenchmarkRun(&runner.Options{}); err != nil {
		t.Fatalf("unrelated policy should ignore replay overrides: %v", err)
	}
	t.Setenv("PK_BENCH_POLICY", "replay-compaction-current")
	if _, err := beginBenchmarkRun(&runner.Options{}); err == nil {
		t.Fatal("replay policy should reject malformed replay override")
	}
}

type subagentSchemaHookAdapter struct {
	gotTools []string
	calls    int
}

func (a *subagentSchemaHookAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.calls++
	for _, definition := range request.Tools {
		a.gotTools = append(a.gotTools, definition.Name)
	}
	return llm.Response{ID: "schema-hook", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "done"}}}}, nil
}

func TestSubagentSchemaPoliciesMeasureRealRunnerDefinitions(t *testing.T) {
	for _, tc := range []struct {
		policy string
		want   map[string]bool
	}{
		{"subagent-schema-current", map[string]bool{"SubagentStart": true, "SubagentStatus": true, "SubagentSend": true, "SubagentWait": true, "SubagentCancel": true}},
		{"compact-subagent-schema", map[string]bool{"Subagent": true}},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			t.Setenv("PK_BENCH_POLICY", tc.policy)
			t.Setenv("PK_BENCH_CONTEXT_METRICS_FILE", "")
			manager, err := subagents.New(subagents.Config{Workspace: t.TempDir(), Runner: func(context.Context, runner.Options) (runner.RunResult, error) {
				return runner.RunResult{}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			adapter := &subagentSchemaHookAdapter{}
			var output bytes.Buffer
			options := runner.Options{
				Adapter: adapter, Prompt: "measure tool schema", Workspace: t.TempDir(), SessionDir: t.TempDir(),
				JSONL: true, Output: &output,
				RegistryFactory: func(runner.ToolRegistryOptions) (tool.Registry, []tool.Skill, []error) {
					return helperregistry.DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), manager), nil, nil
				},
			}
			finish, err := beginBenchmarkRun(&options)
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := runner.Run(context.Background(), options)
			finish()
			if runErr != nil || strings.TrimSpace(result.Text) != "done" {
				t.Fatalf("runner result=%+v err=%v", result, runErr)
			}
			got := map[string]bool{}
			for _, name := range adapter.gotTools {
				got[name] = true
			}
			if len(got) != len(tc.want) {
				t.Fatalf("wire tool names=%v, want=%v", got, tc.want)
			}
			for name := range tc.want {
				if !got[name] {
					t.Fatalf("wire tool %q missing: %v", name, got)
				}
			}
			var metric map[string]any
			for _, line := range strings.Split(output.String(), "\n") {
				var event map[string]any
				if json.Unmarshal([]byte(line), &event) == nil && event["type"] == "benchmark_subagent_schema" {
					metric = event
				}
			}
			if metric == nil || metric["subagent_schema_metrics_available"] != true {
				t.Fatalf("subagent schema metric missing or unavailable: %s", output.String())
			}
			if tc.policy == "subagent-schema-current" && metric["subagent_tool_definition_bytes_saved"] != float64(0) {
				t.Fatalf("current policy should report zero byte delta: %v", metric)
			}
			if tc.policy == "compact-subagent-schema" && metric["subagent_tool_definition_bytes_saved"].(float64) <= 0 {
				t.Fatalf("compact policy did not report savings: %v", metric)
			}
		})
	}
}

func TestSubagentSchemaCompactPolicyFailsClosedWithoutTools(t *testing.T) {
	t.Setenv("PK_BENCH_POLICY", "compact-subagent-schema")
	t.Setenv("PK_BENCH_CONTEXT_METRICS_FILE", "")
	adapter := &subagentSchemaHookAdapter{}
	options := runner.Options{
		Adapter: adapter, Prompt: "must not reach provider", Workspace: t.TempDir(), SessionDir: t.TempDir(),
		JSONL: true, Output: &bytes.Buffer{},
		RegistryFactory: func(runner.ToolRegistryOptions) (tool.Registry, []tool.Skill, []error) {
			return tool.NewRegistry(tool.StaticTranslators{}), nil, nil
		},
	}
	finish, err := beginBenchmarkRun(&options)
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := runner.Run(context.Background(), options)
	finish()
	if runErr == nil || adapter.calls != 0 {
		t.Fatalf("policy must fail closed before provider request: err=%v calls=%d", runErr, adapter.calls)
	}
}

func TestLargeReplayProfileEmitsExactManipulationMetadata(t *testing.T) {
	t.Setenv("PK_BENCH_POLICY", "compact-replayed-shell-output-large")
	t.Setenv("PK_BENCH_REPLAY_THRESHOLD_BYTES", "16384")
	t.Setenv("PK_BENCH_REPLAY_EXCERPT_RUNES", "2048")
	var output bytes.Buffer
	options := runner.Options{JSONL: true, Output: &output}
	finish, err := beginBenchmarkRun(&options)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	for _, expected := range []string{`"mode":"compact-replayed-shell-output-large"`, `"threshold_bytes":16384`, `"excerpt_runes_each_side":2048`, `"eligible_bash_results":0`, `"compacted_bash_results":0`, `"context_compaction_metrics_available":true`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("benchmark metadata missing %s: %s", expected, output.String())
		}
	}
}
