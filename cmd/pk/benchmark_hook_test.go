//go:build pkbench

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type benchmarkHookAdapter struct{}

func (benchmarkHookAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	for _, item := range request.Input {
		if message, ok := item.Data.(llm.Message); ok && message.Text == "HOOK_PRIVATE_SENTINEL" {
			return llm.Response{ID: "response-hook-test", Stop: llm.StopComplete, Usage: llm.Usage{Raw: json.RawMessage(`{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}`)}, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "HOOK_FINAL_SENTINEL"}}}}, nil
		}
	}
	return llm.Response{}, context.Canceled
}

func TestBenchmarkContextHookWritesPrivateCountOnlyMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	t.Setenv("PK_BENCH_POLICY", "")
	t.Setenv("PK_BENCH_CONTEXT_METRICS_FILE", path)
	options := runner.Options{Adapter: benchmarkHookAdapter{}, Workspace: t.TempDir(), SessionDir: filepath.Join(t.TempDir(), "sessions"), Prompt: "HOOK_PRIVATE_SENTINEL"}
	finish, err := beginBenchmarkRun(&options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), options)
	if err != nil || strings.TrimSpace(result.Text) != "HOOK_FINAL_SENTINEL" {
		t.Fatalf("runner result=%+v err=%v", result, err)
	}
	finish()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metrics mode=%o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["type"] != "benchmark_context" || record["response_id"] != "response-hook-test" {
		t.Fatalf("record=%s", data)
	}
	usage, ok := record["usage"].(map[string]any)
	if !ok || usage["input_tokens_available"] != true || usage["input_tokens"] != float64(0) || usage["output_tokens_available"] != true || usage["output_tokens"] != float64(0) || usage["cached_input_tokens_available"] != true || usage["cached_input_tokens"] != float64(0) {
		t.Fatalf("explicit zero usage was not preserved: %v", record["usage"])
	}
	if string(data) == "" || containsBytes(data, []byte("HOOK_PRIVATE_SENTINEL")) || containsBytes(data, []byte("HOOK_FINAL_SENTINEL")) {
		t.Fatalf("request content leaked: %s", data)
	}
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
