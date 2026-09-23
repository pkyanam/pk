package main

import (
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

func TestAssistantHistoryPreviewMatchesJoinedTail(t *testing.T) {
	large := strings.Repeat("λ界🙂abc", 2100)
	cases := [][]llm.Item{
		nil,
		{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "  keep whitespace  "}}},
		{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "analysis", Text: "hidden"}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "ignore"}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "commentary", Text: "first\n"}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: "second"}},
		},
		{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: large}}},
		{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("a", 8191)}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "🙂"}},
		},
		{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: string([]byte{'x', 0xff, 0xfe, 'y'})}}},
		{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: string([]byte{'x', 0xff, 'y'})}}},
		{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: string([]byte{'x', 0xff})}},
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: string([]byte{0xfe, 'y'})}},
		},
	}
	for i, outputs := range cases {
		want, wantTruncated := referenceAssistantHistoryPreview(outputs, maxHistoryEntryBytes)
		got, gotTruncated := assistantHistoryPreview(outputs, maxHistoryEntryBytes)
		if got != want || gotTruncated != wantTruncated {
			t.Errorf("case %d preview differs: got truncated=%v len=%d, want truncated=%v len=%d", i, gotTruncated, len(got), wantTruncated, len(want))
		}
	}
}

func TestAssistantHistoryPreviewLeavesShortMalformedUTF8Untouched(t *testing.T) {
	short := string([]byte{'x', 0xff, 'y'})
	outputs := []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: short}}}
	got, truncated := assistantHistoryPreview(outputs, maxHistoryEntryBytes)
	if got != short || truncated {
		t.Fatalf("short invalid string changed: got %q truncated=%v, want original bytes", got, truncated)
	}
	if got, want := referenceAssistantHistoryPreview(outputs, maxHistoryEntryBytes); got != short || want {
		t.Fatalf("reference is not the old under-limit behavior: got %q truncated=%v", got, want)
	}
}

func TestProjectHistoryMarksLargeAssistantPreviewTruncated(t *testing.T) {
	text := strings.Repeat("result λ ", 4096)
	response := sessionstore.ModelResponse{Response: llm.Response{Output: []llm.Item{{
		Type: llm.ItemMessage,
		Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: text},
	}}}}
	items := []sessionstore.Item{{Sequence: 1, Kind: sessionstore.ItemModelResponse, Data: response}}
	entries, _, truncated := projectHistory(items)
	want, _ := referenceAssistantHistoryPreview(response.Response.Output, maxHistoryEntryBytes)
	if len(entries) != 1 || entries[0].Role != "assistant" || entries[0].Text != want || !truncated || len(entries[0].Text) > maxHistoryEntryBytes {
		t.Fatalf("projected preview mismatch: entries=%+v truncated=%v", entries, truncated)
	}
}

func BenchmarkAssistantHistoryPreviewLarge(b *testing.B) {
	text := strings.Repeat("const payload = \"large-history-value🙂\"\n", 1<<11)
	outputs := make([]llm.Item, 32)
	for i := range outputs {
		outputs[i] = llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: text}}
	}
	b.Run("joined-reference", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			referenceAssistantHistoryPreview(outputs, maxHistoryEntryBytes)
		}
	})
	b.Run("bounded-tail", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			assistantHistoryPreview(outputs, maxHistoryEntryBytes)
		}
	})
}

func referenceAssistantHistoryPreview(outputs []llm.Item, maxBytes int) (string, bool) {
	var parts []string
	for _, output := range outputs {
		if output.Type != llm.ItemMessage {
			continue
		}
		message, ok := output.Data.(llm.Message)
		if ok && message.Role == llm.RoleAssistant && message.Phase != "analysis" && strings.TrimSpace(message.Text) != "" {
			parts = append(parts, message.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	joined := strings.Join(parts, "\n")
	if len(joined) > maxBytes {
		return tailUTF8(joined, maxBytes), true
	}
	return joined, false
}
