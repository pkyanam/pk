package runner

import (
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// BenchmarkHistoryCutLongTask measures local policy work only. It does not
// include network, model latency, or summarization quality.
func BenchmarkHistoryCutLongTask(b *testing.B) {
	request := llm.Request{Input: []llm.Item{
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleSystem, Text: "Stable instructions"}},
		{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "Finish the requested implementation and verify it."}},
	}}
	for i := 0; i < 500; i++ {
		request.Input = append(request.Input, llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: strings.Repeat("completed inspection evidence ", 40)}})
	}
	policy := DefaultHistoryCompactionOptions()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cut, _ := chooseHistoryCut(request, 1, 50_000, policy)
		if cut <= 1 {
			b.Fatal("expected a compactable prefix")
		}
	}
}
