package runner

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRunMarksUnavailableUsageWithoutInventingZeroTokens(t *testing.T) {
	var output bytes.Buffer
	_, err := Run(t.Context(), Options{Prompt: "answer", Workspace: t.TempDir(), SessionDir: t.TempDir(), Adapter: &contextWarningTestAdapter{}, JSONL: true, Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range bytes.Split(output.Bytes(), []byte("\n")) {
		var event map[string]any
		if json.Unmarshal(line, &event) != nil || event["type"] != "usage" {
			continue
		}
		found = true
		if event["response_id"] != "context-warning-answer" || event["usage_available"] != false {
			t.Fatalf("unknown usage marker: %v", event)
		}
		for _, field := range []string{"input_tokens", "output_tokens", "cached_input_tokens"} {
			if _, exists := event[field]; exists {
				t.Fatalf("unavailable usage invented %s: %v", field, event)
			}
		}
	}
	if !found {
		t.Fatal("response without provider usage vanished from accounting")
	}
}
