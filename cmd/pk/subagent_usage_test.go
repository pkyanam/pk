package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestConfiguredSubagentUsageIsSafelyForwardedAlongsideParentJSONL(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	var out strings.Builder
	parent := runner.Options{
		Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low",
		JSONL: true, Output: synchronizedJSONLOutput(&out),
	}
	manager, err := configureSubagents(context.Background(), &parent, subagentRuntimeConfig{
		Workspace: workspace, SessionDir: sessions, Diagnostics: io.Discard,
		Events: subagentJSONLForwarder(parent.Output, parent.JSONL),
		AdapterFactory: func(context.Context, bool, string) (llm.Adapter, error) {
			return &mockModelAdapter{}, nil
		},
		Run: func(_ context.Context, options runner.Options) (runner.RunResult, error) {
			enc := json.NewEncoder(options.Output)
			if err := enc.Encode(map[string]any{"type": "assistant", "response_id": "r-1", "text": "private child response text"}); err != nil {
				return runner.RunResult{}, err
			}
			if err := enc.Encode(map[string]any{
				"type": "usage", "session_id": "private-child-session", "response_id": "r-1",
				"input_tokens": 11, "output_tokens": 5, "reasoning_tokens": 2,
				"cached_input_tokens": 3, "cached_input_tokens_available": true,
				"cache_write_input_tokens": 0, "cache_write_input_tokens_available": true,
				"usage_available": true,
			}); err != nil {
				return runner.RunResult{}, err
			}
			// Unknown usage must remain unknown, not be reported as zero usage.
			if err := enc.Encode(map[string]any{"type": "usage", "session_id": "private-child-session", "response_id": "r-2", "usage_available": false}); err != nil {
				return runner.RunResult{}, err
			}
			// Invalid counters are rejected rather than propagated to the CLI.
			if err := enc.Encode(map[string]any{"type": "usage", "response_id": "r-invalid", "input_tokens": -1, "usage_available": true}); err != nil {
				return runner.RunResult{}, err
			}
			return runner.RunResult{Text: "private child response text"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	var parentWrites sync.WaitGroup
	parentWrites.Add(1)
	go func() {
		defer parentWrites.Done()
		enc := json.NewEncoder(parent.Output)
		for i := 0; i < 40; i++ {
			_ = enc.Encode(map[string]any{"type": "parent_marker", "index": i})
		}
	}()
	child, err := manager.Launch(context.Background(), subagents.LaunchRequest{Task: "inspect workspace", TaskOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Wait(context.Background(), child.ID); err != nil {
		t.Fatal(err)
	}
	parentWrites.Wait()

	counts := map[string]int{}
	usageByResponse := map[string]map[string]any{}
	var unavailableEvent map[string]any
	var terminalState map[string]any
	scanner := bufio.NewScanner(strings.NewReader(out.String()))
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("concurrent JSONL write produced malformed line %q: %v", scanner.Text(), err)
		}
		typ, _ := event["type"].(string)
		counts[typ]++
		if typ == "subagent_usage" {
			if event["child_id"] != child.ID {
				t.Fatalf("usage marker child_id=%v want=%s", event["child_id"], child.ID)
			}
			responseID, _ := event["response_id"].(string)
			usageByResponse[responseID] = event
		}
		if typ == "subagent_accounting_unavailable" {
			unavailableEvent = event
		}
		if typ == "subagent_state" {
			terminalState = event
		}
		if _, exists := event["session_id"]; exists {
			t.Fatalf("child session id leaked into parent CLI stream: %v", event)
		}
		if strings.Contains(scanner.Text(), "private child response text") || strings.Contains(scanner.Text(), "private-child-session") {
			t.Fatalf("child content leaked into parent CLI stream: %s", scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if counts["parent_marker"] != 40 || counts["subagent_started"] != 1 || counts["subagent_usage"] != 2 || counts["subagent_accounting_unavailable"] != 1 || counts["subagent_state"] != 1 {
		t.Fatalf("forwarded event counts=%v", counts)
	}
	if terminalState == nil || terminalState["child_id"] != child.ID || terminalState["state"] != "completed" {
		t.Fatalf("child terminal state=%v, want completed for %s", terminalState, child.ID)
	}
	available := usageByResponse["r-1"]
	if available == nil || available["input_tokens"] != float64(11) || available["output_tokens"] != float64(5) || available["usage_available"] != true {
		t.Fatalf("available usage record=%v", available)
	}
	unknown := usageByResponse["r-2"]
	if unknown == nil || unknown["usage_available"] != false {
		t.Fatalf("missing-usage marker=%v", unknown)
	}
	if _, exists := unknown["input_tokens"]; exists {
		t.Fatalf("unknown usage was incorrectly converted to a token count: %v", unknown)
	}
	if unavailableEvent == nil || unavailableEvent["response_id"] != "r-invalid" {
		t.Fatalf("invalid usage was not marked unavailable with its response identity: %v", unavailableEvent)
	}
	if counts["subagent_response"] != 0 || counts["assistant"] != 0 {
		t.Fatalf("unapproved child events were copied to parent stream: %v", counts)
	}
}
