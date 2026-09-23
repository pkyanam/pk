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
	manager, ctx := largeSessionListFixture(b)
	b.ReportMetric(32, "sessions/op")
	b.ReportMetric(32*(128<<10), "assistant-output-bytes/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := manager.List(ctx, ListOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListRepeatedLargeSessionLogs measures steady-state repeated list
// calls after the first validated metadata summary has populated the cache.
func BenchmarkListRepeatedLargeSessionLogs(b *testing.B) {
	manager, ctx := largeSessionListFixture(b)
	if _, err := manager.List(ctx, ListOptions{}); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(32, "sessions/op")
	b.ReportMetric(32*(128<<10), "assistant-output-bytes/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := manager.List(ctx, ListOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetLargeSessionLogCold invalidates only the in-memory summary entry
// before each lookup, measuring uncached Get work over one 128 KiB response.
func BenchmarkGetLargeSessionLogCold(b *testing.B) {
	manager, ctx := largeSessionListFixture(b)
	const id = "bench-session-000"
	b.ReportMetric(128<<10, "assistant-output-bytes/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		invalidateMetadataCache(manager.SessionDir, id)
		if _, err := manager.Get(ctx, id, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func largeSessionListFixture(b *testing.B) (Manager, context.Context) {
	b.Helper()
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
	return Manager{SessionDir: sessionDir}, ctx
}
