package helperregistry

import (
	"context"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func TestSubagentStartToolAdvertisesExplicitTaskOnlyAndLimits(t *testing.T) {
	manager, err := subagents.New(subagents.Config{Workspace: t.TempDir(), Runner: func(context.Context, runner.Options) (runner.RunResult, error) {
		return runner.RunResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	registry := DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), manager)
	var definition tool.Definition
	for _, item := range registry.StaticDefinitions() {
		if item.Tool.Name == "SubagentStart" {
			definition = item
		}
	}
	if definition.Tool.Name == "" {
		t.Fatal("SubagentStart definition missing")
	}
	if !strings.Contains(definition.Tool.Description, "2 active") || !strings.Contains(definition.Tool.Description, "not a sandbox") || !strings.Contains(definition.Tool.Description, "task_only=true") {
		t.Fatalf("start description omits limits or permission boundary: %q", definition.Tool.Description)
	}
	params := definition.Tool.Parameters
	if params["type"] != "object" {
		t.Fatalf("schema=%v", params)
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok || properties["task_only"] == nil || properties["files"] == nil {
		t.Fatalf("schema properties=%v", params)
	}
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("schema required=%T %v", params["required"], params["required"])
	}
	if len(required) != 2 || required[0] != "task" || required[1] != "task_only" {
		t.Fatalf("task_only must be explicit; required=%v", required)
	}
}
