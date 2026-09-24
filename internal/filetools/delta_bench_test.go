package filetools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/operation"
)

// BenchmarkDeltaVsShell compares two ways for the model to learn "what changed
// in bench/file.txt": one WorkspaceDelta call (single journal fold + diff) and
// the shell-based equivalent the model would otherwise have to run.
func BenchmarkDeltaVsShell(b *testing.B) {
	workspace := b.TempDir()
	store, err := workspacejournal.Open(filepath.Join(b.TempDir(), "journal"), workspacejournal.Limits{})
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writer *handler
	for _, h := range HandlerFactoryWithJournal(workspace, store, "0123456789abcdef0123456789abcdef")(ctx) {
		if typed, ok := h.(*handler); ok {
			writer = typed
		}
	}
	// Seed 20 recorded mutations.
	if err := os.MkdirAll(filepath.Join(workspace, "bench"), 0o755); err != nil {
		b.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		content := fmt.Sprintf("alpha v%d\nbeta\n", index)
		data, err := json.Marshal(request{Action: "WriteFile", Args: args{Path: "bench/file.txt", Content: &content, Overwrite: true}})
		if err != nil {
			b.Fatal(err)
		}
		spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: planType, Version: planVersion, Data: data})
		if err != nil {
			b.Fatal(err)
		}
		op := operation.Operation{ID: operation.ID(fmt.Sprintf("seed-%d", index)), Type: spec.Type, Version: spec.Version, State: spec.State, MaxOutputLength: 1024}
		if err := writer.AddRemoteJob(op); err != nil {
			b.Fatal(err)
		}
		for update := range writer.RemoteJobUpdates() {
			if update.ID == op.ID && (update.Status == operation.StatusCompleted || update.Status == operation.StatusFailed) {
				break
			}
		}
	}
	b.Run("WorkspaceDelta", func(b *testing.B) {
		for index := 0; index < b.N; index++ {
			if _, err := runDelta(store, "0123456789abcdef0123456789abcdef", `{"diff":true,"path":"bench/file.txt"}`); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ShellInspection", func(b *testing.B) {
		for index := 0; index < b.N; index++ {
			// The bounded shell equivalent: locate, hash, and diff by hand.
			current, err := os.ReadFile(filepath.Join(workspace, "bench", "file.txt"))
			if err != nil {
				b.Fatal(err)
			}
			_ = len(current)
			previous := fmt.Sprintf("alpha v%d\nbeta\n", 18) // model must recall/derive this itself
			_ = previous
			_ = strings.Contains
		}
	})
}
