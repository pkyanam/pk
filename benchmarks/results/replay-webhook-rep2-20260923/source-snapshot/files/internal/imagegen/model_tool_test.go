package imagegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type submitter struct{ spec operation.Spec }

func (s *submitter) Submit(spec operation.Spec) operation.ID { s.spec = spec; return "image-op" }

func TestDecoratorRequiresExplicitDriverAndPreservesBaseRegistry(t *testing.T) {
	base := tool.NewRegistry(tool.StaticTranslators{})
	if got := Decorator(Config{}, t.TempDir())(base); got != base {
		t.Fatal("ImageGen was enabled without an explicit driver")
	}
	decorated := Decorator(Config{Driver: "gpt-6-astra"}, t.TempDir())(base)
	if _, ok := decorated.Resolve(ToolName); !ok {
		t.Fatal("ImageGen translator was not registered")
	}
	defs := decorated.StaticDefinitions()
	if len(defs) != 1 || defs[0].Tool.Name != ToolName {
		t.Fatalf("unexpected definitions: %+v", defs)
	}
}

func TestImageGenTranslatorSubmitsAsyncRemoteJobAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	translator := imageTranslator{root: root, config: Config{Driver: "gpt-6-astra"}}
	ctx := &submitter{}
	status := translator.Translate(ctx, llm.ToolCall{CallID: "call", Arguments: `{"prompt":"mint leaf","output_path":"art/leaf.png"}`})
	if status.Error != "" || len(status.WaitingFor) != 1 || status.WaitingFor[0] != "image-op" {
		t.Fatalf("unexpected translation: %+v", status)
	}
	state, err := operation.DecodeRemoteJobState(operation.Operation{Type: ctx.spec.Type, Version: ctx.spec.Version, MaxOutputLength: ctx.spec.MaxOutputLength, State: ctx.spec.State})
	if err != nil || state.Plan.Type != planType {
		t.Fatalf("remote job plan = %+v, %v", state.Plan, err)
	}
	var args toolArguments
	if err := json.Unmarshal(state.Plan.Data, &args); err != nil {
		t.Fatal(err)
	}
	if args.OutputPath != "art/leaf.png" {
		t.Fatalf("output path = %q", args.OutputPath)
	}
	ready := operation.Operation{ID: "image-op", Type: ctx.spec.Type, Version: ctx.spec.Version, Status: operation.StatusReady, MaxOutputLength: ctx.spec.MaxOutputLength, State: ctx.spec.State}
	progress, err := translator.TranslateResult("call", tool.CallStatus{WaitingFor: []operation.ID{ready.ID}}, []operation.Operation{ready})
	if err != nil || len(progress.Output) != 1 || progress.Output[0].Value != "Image generation is still running." {
		t.Fatalf("pending result = %+v, %v", progress, err)
	}
	bad := translator.Translate(&submitter{}, llm.ToolCall{Arguments: `{"prompt":"x","output_path":"../escape.png"}`})
	if bad.Error == "" || !strings.Contains(bad.Error, "escapes workspace") {
		t.Fatalf("escape was accepted: %+v", bad)
	}
	abs := translator.Translate(&submitter{}, llm.ToolCall{Arguments: `{"prompt":"x","reference_paths":["/etc/passwd"]}`})
	if abs.Error == "" || !strings.Contains(abs.Error, "relative to the task workspace") {
		t.Fatalf("absolute reference was accepted: %+v", abs)
	}
}

func TestImageGenRemoteJobCancellationStopsWorkerProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell and process IDs")
	}
	root, codexHome := t.TempDir(), t.TempDir()
	marker := filepath.Join(t.TempDir(), "pid")
	thread := "01a0cc58-0b27-7c20-8f9b-3efe81d0553b"
	fixture := filepath.Join(t.TempDir(), "slow-codex")
	script := "#!/bin/sh\nprintf '%s' $$ > '" + marker + "'\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\nexec sleep 30\n"
	if err := os.WriteFile(fixture, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(toolArguments{Prompt: "mint leaf", OutputPath: "leaf.png"})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: planType, Version: planVersion, Data: args})
	if err != nil {
		t.Fatal(err)
	}
	op := operation.Operation{ID: "image-cancel-test", Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, MaxOutputLength: spec.MaxOutputLength, State: spec.State}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := newHandler(ctx, Config{Executable: fixture, CodexHome: codexHome, Driver: "gpt-6-astra", Timeout: time.Minute}, root)
	manager := operation.NewLocalOperationManager(ctx, handler)
	defer func() {
		cancel()
		for range manager.Updates() {
		}
	}()
	if err := manager.Add(op); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(marker)
		if readErr == nil {
			_, _ = fmt.Sscanf(string(data), "%d", &pid)
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("image worker did not start")
	}
	if err := manager.Cancel(op.ID, "test cancellation"); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-manager.Updates():
		if update.ID != op.ID || update.Status != operation.StatusCanceled {
			t.Fatalf("cancel update = %+v", update)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remote image operation did not settle cancellation")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("image worker process %d survived cancellation: %v", pid, err)
	}
}
