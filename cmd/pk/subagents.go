package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/helperregistry"
	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// subagentRuntimeConfig captures only explicitly inherited parent capabilities.
// Child runs never call configureSubagents, so they cannot create nested agents.
type subagentRuntimeConfig struct {
	Workspace               string
	SessionDir              string
	WorkspaceJournalRoot    string
	Model                   string
	Effort                  string
	CompactCapturedOutput   bool
	ContextBudgetConfig     config.ContextBudgetConfig
	HistoryCompactionConfig config.HistoryCompactionConfig
	ProviderID              string
	ProviderConfig          *providers.Provider
	SkillsDirs              []string
	SystemPrompt            string
	PluginManifests         []string
	MCPServers              []mcpclient.ServerConfig
	InheritPlugins          bool
	InheritMCP              bool
	UseCodex                bool
	CodexPath               string
	Diagnostics             io.Writer
	Events                  func(subagents.Event)
	AdapterFactory          func(context.Context, bool, string) (llm.Adapter, error)
	Run                     func(context.Context, runner.Options) (runner.RunResult, error)
	MaxConcurrent           int
}

// configureSubagents installs a parent-only subagent tool surface. The caller
// owns the returned manager and must Close it after the parent runner settles.
func configureSubagents(ctx context.Context, parent *runner.Options, cfg subagentRuntimeConfig) (*subagents.Manager, error) {
	if parent == nil {
		return nil, errors.New("parent runner options are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Diagnostics == nil {
		cfg.Diagnostics = io.Discard
	}
	if cfg.Workspace == "" {
		cfg.Workspace = parent.Workspace
	}
	if cfg.SessionDir == "" {
		cfg.SessionDir = parent.SessionDir
	}
	// Children inherit the parent's context policy unless the caller has
	// explicitly supplied a different runtime configuration.
	if !cfg.CompactCapturedOutput {
		cfg.CompactCapturedOutput = parent.CompactCapturedOutput
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = parent.SystemPrompt
	}
	if len(cfg.SkillsDirs) == 0 {
		cfg.SkillsDirs = append([]string(nil), parent.SkillsDirs...)
	}
	previousOnSession := parent.OnSession
	parent.OnSession = func(id string) {
		parent.SessionID = id
		if previousOnSession != nil {
			previousOnSession(id)
		}
	}
	if cfg.ProviderID == "" {
		cfg.ProviderID = parent.ProviderID
	}
	if cfg.ProviderConfig != nil {
		if cfg.ProviderID != "" && cfg.ProviderID != cfg.ProviderConfig.ID {
			return nil, errors.New("subagent provider configuration does not match the selected provider ID")
		}
		cfg.ProviderID = cfg.ProviderConfig.ID
	}
	if cfg.ProviderID != "" && cfg.ProviderConfig == nil {
		providerConfig, err := loadCLIProvider(cfg.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("load subagent provider %q: %w", cfg.ProviderID, err)
		}
		cfg.ProviderConfig = providerConfig
	}
	if cfg.Run == nil {
		cfg.Run = runner.Run
	}
	if cfg.AdapterFactory == nil {
		cfg.AdapterFactory = func(ctx context.Context, useCodex bool, codexPath string) (llm.Adapter, error) {
			if cfg.ProviderConfig != nil {
				return providers.NewClient(*cfg.ProviderConfig)
			}
			return prepareAdapter(ctx, useCodex, codexPath)
		}
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		if cfg.ProviderConfig != nil && strings.TrimSpace(cfg.ProviderConfig.DefaultModel) != "" {
			model = cfg.ProviderConfig.DefaultModel
		} else if cfg.ProviderID != "" && parent.ProviderID == cfg.ProviderID && strings.TrimSpace(parent.Model) != "" {
			model = parent.Model
		} else {
			model = config.DefaultModel
		}
	}
	effort := strings.TrimSpace(cfg.Effort)
	if effort == "" {
		if cfg.ProviderConfig != nil && strings.TrimSpace(cfg.ProviderConfig.DefaultEffort) != "" {
			effort = cfg.ProviderConfig.DefaultEffort
		} else {
			effort = strings.TrimSpace(parent.Effort)
		}
	}
	if effort == "" {
		effort = config.DefaultEffort
	}
	manager, err := subagents.New(subagents.Config{
		Workspace: cfg.Workspace, SessionDir: cfg.SessionDir,
		ParentSessionID:      func() string { return parent.SessionID },
		WorkspaceJournalRoot: cfg.WorkspaceJournalRoot,
		Model:                model, Effort: effort,
		SystemPrompt: cfg.SystemPrompt, SkillsDirs: append([]string(nil), cfg.SkillsDirs...),
		MaxConcurrent: cfg.MaxConcurrent, Depth: 0, Events: cfg.Events,
		Runner: func(runCtx context.Context, child runner.Options) (runner.RunResult, error) {
			child.CompactCapturedOutput = cfg.CompactCapturedOutput
			child.ProviderID = cfg.ProviderID
			if cfg.ProviderConfig != nil {
				child.ProviderFingerprint = cfg.ProviderConfig.Fingerprint()
			}
			return runSubagent(runCtx, child, cfg)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create subagent manager: %w", err)
	}
	parent.DecorateRegistry = composeRegistryDecorators(parent.DecorateRegistry, func(base tool.Registry) tool.Registry {
		return helperregistry.DecorateRegistry(base, manager)
	})
	parent.RemoteJobHandlers = composeRemoteJobHandlers(parent.RemoteJobHandlers, func(handlerCtx context.Context) []operation.RemoteJobHandler {
		return helperregistry.RemoteJobHandlers(handlerCtx, manager)
	})
	return manager, nil
}

func configuredSubagentMCPServers() ([]mcpclient.ServerConfig, error) {
	return (mcpclient.ConfigStore{Home: pkHome()}).List()
}

func runSubagent(ctx context.Context, child runner.Options, cfg subagentRuntimeConfig) (result runner.RunResult, runErr error) {
	adapter, err := cfg.AdapterFactory(ctx, cfg.UseCodex, cfg.CodexPath)
	if err != nil {
		return runner.RunResult{}, fmt.Errorf("prepare subagent model client: %w", err)
	}
	if closer, ok := adapter.(interface{ Close() error }); ok {
		defer func() {
			if closeErr := closer.Close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close subagent model client: %w", closeErr))
			}
		}()
	}
	child.Adapter = adapter
	providerBaseURL := ""
	if cfg.ProviderConfig != nil {
		providerBaseURL = cfg.ProviderConfig.BaseURL
	}
	if err := applyConfiguredContextManagement(&child, config.Config{ContextBudget: cfg.ContextBudgetConfig, HistoryCompaction: cfg.HistoryCompactionConfig}, providerBaseURL); err != nil {
		return runner.RunResult{}, fmt.Errorf("resolve subagent context budget: %w", err)
	}
	child.ToolEvents = true
	child.JSONL = true
	child.Diagnostics = cfg.Diagnostics
	manifests := []string(nil)
	if cfg.InheritPlugins {
		manifests = append(manifests, cfg.PluginManifests...)
	}
	pluginHost, err := configureCLIExtensionsQuiet(ctx, &child, manifests, nil, cfg.Diagnostics, tinyFishRegistryExtension(ctx))
	if err != nil {
		return runner.RunResult{}, fmt.Errorf("load subagent extensions: %w", err)
	}
	if pluginHost != nil {
		defer func() {
			if closeErr := pluginHost.Close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close subagent plugins: %w", closeErr))
			}
		}()
	}
	var finalizePlugins func() error
	if cfg.InheritPlugins && len(cfg.PluginManifests) > 0 {
		finalizePlugins, err = prepareRPCPluginSession(&child, pluginHost)
		if err != nil {
			return runner.RunResult{}, err
		}
	}
	if cfg.InheritMCP && len(cfg.MCPServers) > 0 {
		mcpHost, report, err := mcpclient.NewHost(ctx, cfg.MCPServers)
		if err != nil {
			return runner.RunResult{}, fmt.Errorf("load inherited MCP servers for subagent: %w", err)
		}
		for _, warning := range report.Warnings {
			fmt.Fprintf(cfg.Diagnostics, "pk: subagent MCP warning: %s\n", warning)
		}
		defer func() {
			if closeErr := mcpHost.Close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close subagent MCP servers: %w", closeErr))
			}
		}()
		child.DecorateRegistry = composeRegistryDecorators(child.DecorateRegistry, func(base tool.Registry) tool.Registry {
			decorated, issues := mcpclient.DecorateRegistry(base, mcpHost)
			for _, issue := range issues {
				fmt.Fprintf(cfg.Diagnostics, "pk: subagent MCP warning: %v\n", issue)
			}
			return decorated
		})
		child.RemoteJobHandlers = composeRemoteJobHandlers(child.RemoteJobHandlers, mcpHost.RemoteJobHandlers)
		child.MCPFingerprint = mcpHost.SchemaFingerprint()
	}
	result, runErr = cfg.Run(ctx, child)
	if finalizePlugins != nil {
		if finalErr := finalizePlugins(); runErr == nil && finalErr != nil {
			runErr = finalErr
		}
	}
	return result, runErr
}
