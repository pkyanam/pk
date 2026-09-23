package sessionmanager

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"encoding/json/jsontext"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

// BenchmarkListLargeSessionLogs exposes the cost of rendering searchable
// metadata when a history contains large captured assistant/tool outputs.
// Setup is outside the timer; run with -cpuprofile to inspect the list path.
func BenchmarkListLargeSessionLogs(b *testing.B) {
	const (
		sessionCount = 32
		outputBytes  = 128 << 10
	)
	sessionDir := b.TempDir()
	store, err := localfile.New(sessionDir)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	answer := strings.Repeat("x", outputBytes)
	for i := 0; i < sessionCount; i++ {
		id := session.ID(fmt.Sprintf("bench-session-%03d", i))
		if _, err := store.Create(ctx, id); err != nil {
			b.Fatal(err)
		}
		if err := store.AppendInput(ctx, id, inbox.Input{ID: inbox.ID("prompt"), Kind: inbox.InputExternal, Payload: jsontext.Value(`"benchmark history"`)}); err != nil {
			b.Fatal(err)
		}
		if err := store.AppendTurn(ctx, id, session.Turn{ID: "turn"}); err != nil {
			b.Fatal(err)
		}
		if err := store.AppendModelResponse(ctx, id, sessionstore.ModelResponse{
			TurnID: "turn", Response: llm.Response{ID: "response", Output: []llm.Item{{
				Type: llm.ItemMessage,
				Data: llm.Message{Role: llm.RoleAssistant, Phase: "final_answer", Text: answer},
			}}},
		}); err != nil {
			b.Fatal(err)
		}
	}
	manager := Manager{SessionDir: sessionDir}
	b.ReportMetric(sessionCount, "sessions/op")
	b.ReportMetric(sessionCount*outputBytes, "assistant-output-bytes/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := manager.List(ctx, ListOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}
