//go:build pkbench

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

type benchmarkHookAdapter struct{}

func (benchmarkHookAdapter) Respond(context.Context, llm.Request, llm.RequestOptions) (llm.Response, error) {
	return llm.Response{ID: "response-hook-test"}, nil
}

func TestBenchmarkContextHookWritesPrivateCountOnlyMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	t.Setenv("PK_BENCH_POLICY", "")
	t.Setenv("PK_BENCH_CONTEXT_METRICS_FILE", path)
	options := runner.Options{Adapter: benchmarkHookAdapter{}}
	finish, err := beginBenchmarkRun(&options)
	if err != nil {
		t.Fatal(err)
	}
	response, err := options.Adapter.Respond(context.Background(), llm.Request{Input: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "HOOK_PRIVATE_SENTINEL"}}}}, llm.RequestOptions{})
	if err != nil || response.ID != "response-hook-test" {
		t.Fatalf("wrapped respond=%+v err=%v", response, err)
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
	if string(data) == "" || containsBytes(data, []byte("HOOK_PRIVATE_SENTINEL")) {
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
