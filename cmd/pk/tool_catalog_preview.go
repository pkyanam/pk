package main

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	"github.com/pkyanam/pk/internal/helperregistry"
	"github.com/pkyanam/pk/internal/imagegen"
	"github.com/pkyanam/pk/internal/interaction"
	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/pkyanam/pk/internal/websearch"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func (s *rpcServer) previewModelToolCatalog(ctx context.Context, workspace, sessionDir string, skillsDirs []string, hasPlugins bool, imageDriver string) (savedToolCatalogPayload, error) {
	if err := ctx.Err(); err != nil {
		return savedToolCatalogPayload{}, err
	}
	options := runner.Options{Workspace: workspace, SessionDir: sessionDir, SkillsDirs: skillsDirs, WorkspaceJournalRoot: filepath.Join(pkHome(), "journal")}
	broker := interaction.NewBroker(ctx, "")
	defer broker.Close()
	manager, err := subagents.New(subagents.Config{
		Workspace: workspace, SessionDir: sessionDir,
		Runner: func(context.Context, runner.Options) (runner.RunResult, error) {
			return runner.RunResult{}, errors.New("subagent execution is unavailable while previewing tools")
		},
	})
	if err != nil {
		return savedToolCatalogPayload{}, err
	}
	defer manager.Close()
	web := websearch.Decorator(websearch.Config{})
	options.DecorateRegistry = func(base tool.Registry) tool.Registry {
		base = interaction.DecorateRegistry(base, broker)
		base = web(base)
		if imageDriver != "" {
			base = imagegen.Decorator(imagegen.Config{Driver: imageDriver, Effort: "low"}, workspace)(base)
		}
		return helperregistry.DecorateRegistry(base, manager)
	}
	tools, warnings, err := runner.PreviewToolCatalog(ctx, options)
	if err != nil {
		return savedToolCatalogPayload{}, err
	}
	deferred := make([]string, 0, 2)
	if hasPlugins {
		deferred = append(deferred, "enabled plugins")
	}
	servers, err := (mcpclient.ConfigStore{Home: pkHome()}).Summaries()
	if err != nil {
		return savedToolCatalogPayload{}, err
	}
	if len(servers) > 0 {
		deferred = append(deferred, "configured MCP servers")
	}
	sort.Strings(deferred)
	notice := "Preview of the core model tools. The exact registry is saved after the first prompt."
	if len(deferred) > 0 {
		notice = "Core-tool preview. " + joinDeferredTools(deferred) + " load with the first prompt; browsing does not start their processes."
	}
	return savedToolCatalogPayload{
		Tools: tools, Saved: false, Initialized: false, Preview: true,
		Notice: notice, Deferred: deferred, Warnings: warnings,
	}, nil
}

func joinDeferredTools(items []string) string {
	if len(items) == 1 {
		return items[0] + " tools"
	}
	if len(items) == 2 {
		return items[0] + " and " + items[1] + " tools"
	}
	return "Optional integration tools"
}
