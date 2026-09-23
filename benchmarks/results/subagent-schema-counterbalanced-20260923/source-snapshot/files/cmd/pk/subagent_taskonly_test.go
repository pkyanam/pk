package main

import (
	"context"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestTaskOnlySubagentKeepsExplicitExtensionToolsWithoutStartupChatter(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	worker := buildRPCWorkspaceStatsWorker(t)
	manifest := writeRPCPluginManifest(t, t.TempDir(), "workspace-stats", worker, "workspace_stats")
	var diagnostics strings.Builder
	childAdapter := &subagentScriptAdapter{child: true}
	run := func(_ context.Context, options runner.Options) (runner.RunResult, error) {
		registry := tool.NewRegistry(tool.StaticTranslators{})
		if options.DecorateRegistry != nil {
			registry = options.DecorateRegistry(registry)
		}
		if _, ok := registry.Resolve("workspace_stats"); !ok {
			t.Fatal("child task-only runner did not receive explicitly inherited extension tool")
		}
		if options.Workspace != workspace {
			t.Fatalf("child workspace=%q want=%q", options.Workspace, workspace)
		}
		return runner.RunResult{Text: "inspection complete"}, nil
	}
	manager, err := configureSubagents(context.Background(), &runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "low"}, subagentRuntimeConfig{
		Workspace: workspace, SessionDir: sessions, PluginManifests: []string{manifest}, InheritPlugins: true,
		Diagnostics:    &diagnostics,
		AdapterFactory: func(context.Context, bool, string) (llm.Adapter, error) { return childAdapter, nil },
		Run:            run,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	child, err := manager.Launch(context.Background(), subagents.LaunchRequest{Task: "inspect the workspace without claiming file ownership", TaskOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Files) != 0 || !child.TaskOnly {
		t.Fatalf("task-only child metadata=%+v", child)
	}
	if _, err := manager.Wait(context.Background(), child.ID); err != nil {
		t.Fatal(err)
	}
	if !childAdapter.closed {
		t.Fatal("child adapter was not closed")
	}
	if strings.Contains(diagnostics.String(), "loaded extension workspace-stats") {
		t.Fatalf("child emitted repetitive extension load notice: %q", diagnostics.String())
	}
}
