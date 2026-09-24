package filetools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/operation"
)

// benchJournaledWrite measures one WriteFile round trip through the handler
// with journaling disabled or enabled, including the durable prepare fsync.
func benchJournaledWrite(b *testing.B, journaled bool) {
	workspace := b.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "bench"), 0o755); err != nil {
		b.Fatal(err)
	}
	var store *workspacejournal.Store
	if journaled {
		var err error
		store, err = workspacejournal.Open(filepath.Join(b.TempDir(), "journal"), workspacejournal.Limits{MaxObjectBytes: 1 << 30})
		if err != nil {
			b.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writer *handler
	for _, h := range HandlerFactoryWithJournal(workspace, store, "0123456789abcdef0123456789abcdef")(ctx) {
		if typed, ok := h.(*handler); ok {
			writer = typed
		}
	}
	if writer == nil {
		b.Fatal("missing writer handler")
	}
	content := strings.Repeat("x", 4096)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		data, err := json.Marshal(request{Action: "WriteFile", Args: args{Path: "bench/file.txt", Content: &content, Overwrite: true}})
		if err != nil {
			b.Fatal(err)
		}
		spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: planType, Version: planVersion, Data: data})
		if err != nil {
			b.Fatal(err)
		}
		op := operation.Operation{ID: operation.ID("bench-op"), Type: spec.Type, Version: spec.Version, State: spec.State, MaxOutputLength: 1024}
		start := time.Now()
		if err := writer.AddRemoteJob(op); err != nil {
			b.Fatal(err)
		}
		for update := range writer.RemoteJobUpdates() {
			if update.ID == op.ID {
				switch update.Status {
				case operation.StatusCompleted, operation.StatusFailed:
				}
				if update.Status == operation.StatusCompleted || update.Status == operation.StatusFailed {
					break
				}
			}
		}
		if journaled && index == 0 {
			// First iteration includes one-time directory creation; report the
			// steady-state by resetting the timer after the first write.
			b.ResetTimer()
		}
		_ = start
	}
	b.StopTimer()
}

func BenchmarkWriteFileUnjournaled(b *testing.B)   { benchJournaledWrite(b, false) }
func BenchmarkWriteFileJournaled(b *testing.B)     { benchJournaledWrite(b, true) }
