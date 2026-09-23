package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/pkyanam/pk/internal/imagegen"
	"github.com/pkyanam/pk/internal/interaction"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type contextBudgetOverrideRequest struct {
	ProviderID string          `json:"provider_id"`
	ModelID    string          `json:"model_id"`
	Context    json.RawMessage `json:"context_tokens"`
	Input      json.RawMessage `json:"input_tokens"`
	Output     json.RawMessage `json:"output_tokens"`
}

type contextBudgetConfigureRequest struct {
	UnknownInput  json.RawMessage               `json:"unknown_input_budget_tokens"`
	OutputReserve json.RawMessage               `json:"output_reserve_tokens"`
	SafetyMargin  json.RawMessage               `json:"safety_margin_tokens"`
	Compaction    map[string]json.RawMessage    `json:"history_compaction"`
	Override      *contextBudgetOverrideRequest `json:"override"`
}

func (s *rpcServer) handleContextBudgetStatus(requestID string) {
	s.mu.Lock()
	options := s.opts
	started := s.started
	providerID := s.providerID
	s.mu.Unlock()
	if !started {
		_ = s.emit(requestID, "error", map[string]any{"message": "send start before requesting context budget status", "recoverable": true})
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		_ = s.emit(requestID, "error", map[string]any{"message": "load context budget config: " + err.Error(), "recoverable": true})
		return
	}
	budget, err := resolveRPCContextBudget(providerID, options.Model, cfg)
	if err != nil {
		_ = s.emit(requestID, "error", map[string]any{"message": "resolve context budget: " + err.Error(), "recoverable": true})
		return
	}
	baseURL := rpcContextBudgetBaseURL(providerID)
	_ = s.emit(requestID, "context_budget", contextBudgetPayload(budget, cfg, baseURL))
}

func (s *rpcServer) handleContextBudgetConfigure(requestID string, raw json.RawMessage) {
	var request contextBudgetConfigureRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		_ = s.emit(requestID, "error", map[string]any{"message": "invalid context budget settings: " + err.Error(), "recoverable": true})
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		_ = s.emit(requestID, "error", map[string]any{"message": "load context budget config: " + err.Error(), "recoverable": true})
		return
	}
	if err := applyOptionalInt(request.UnknownInput, &cfg.ContextBudget.UnknownInputBudgetTokens); err != nil {
		s.emitContextBudgetError(requestID, err)
		return
	}
	if err := applyOptionalInt(request.OutputReserve, &cfg.ContextBudget.OutputReserveTokens); err != nil {
		s.emitContextBudgetError(requestID, err)
		return
	}
	if err := applyOptionalInt(request.SafetyMargin, &cfg.ContextBudget.SafetyMarginTokens); err != nil {
		s.emitContextBudgetError(requestID, err)
		return
	}
	if err := applyHistoryCompactionPatch(&cfg.HistoryCompaction, request.Compaction); err != nil {
		s.emitContextBudgetError(requestID, err)
		return
	}
	if request.Override != nil {
		if err := applyContextOverridePatch(&cfg.ContextBudget, *request.Override); err != nil {
			s.emitContextBudgetError(requestID, err)
			return
		}
	}
	cfg.ContextBudget = cfg.ContextBudget.Normalized()
	cfg.HistoryCompaction = cfg.HistoryCompaction.Normalized()
	if err := cfg.ContextBudget.Validate(); err != nil {
		s.emitContextBudgetError(requestID, err)
		return
	}
	if err := cfg.HistoryCompaction.Validate(); err != nil {
		s.emitContextBudgetError(requestID, err)
		return
	}

	// Keep the idle check, save, and live option update serialized with prompt
	// submission so a turn cannot start with a half-applied configuration.
	s.mu.Lock()
	if !s.started || s.active || s.skillOperationActive || s.pluginCommandActive || s.releaseActive || s.attachedTask != "" {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "context budget settings can only be changed while the foreground session is idle", "recoverable": true})
		return
	}
	providerID, model := s.providerID, s.opts.Model
	budget, err := resolveRPCContextBudget(providerID, model, cfg)
	if err == nil {
		err = config.Save(s.cfgPath, cfg)
	}
	if err == nil {
		s.opts.ContextBudget = budget
		s.opts.HistoryCompaction = configuredHistoryCompaction(cfg.HistoryCompaction)
		s.contextBudgetConfig = cfg.ContextBudget
		s.historyCompactionConfig = cfg.HistoryCompaction
	}
	s.mu.Unlock()
	if err != nil {
		s.emitContextBudgetError(requestID, err)
		return
	}
	_ = s.emit(requestID, "context_budget_configured", contextBudgetPayload(budget, cfg, rpcContextBudgetBaseURL(providerID)))
}

func (s *rpcServer) emitContextBudgetError(requestID string, err error) {
	_ = s.emit(requestID, "error", map[string]any{"message": err.Error(), "recoverable": true})
}

func applyOptionalInt(raw json.RawMessage, target **int64) error {
	if len(raw) == 0 {
		return nil
	}
	if string(raw) == "null" {
		*target = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("token budget fields must be integers or null")
	}
	*target = &value
	return nil
}

func applyHistoryCompactionPatch(target *config.HistoryCompactionConfig, patch map[string]json.RawMessage) error {
	for key, raw := range patch {
		switch key {
		case "enabled":
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				return errors.New("history_compaction.enabled must be a boolean")
			}
			target.Enabled = &v
		case "trigger_ratio":
			var v float64
			if err := json.Unmarshal(raw, &v); err != nil {
				return errors.New("trigger_ratio must be numeric")
			}
			target.TriggerRatio = &v
		case "target_ratio":
			var v float64
			if err := json.Unmarshal(raw, &v); err != nil {
				return errors.New("target_ratio must be numeric")
			}
			target.TargetRatio = &v
		case "summary_reserve_tokens":
			if err := applyOptionalInt(raw, &target.SummaryReserveTokens); err != nil {
				return err
			}
		case "summary_input_tokens":
			if err := applyOptionalInt(raw, &target.SummaryInputTokens); err != nil {
				return err
			}
		case "max_summary_tokens":
			if err := applyOptionalInt(raw, &target.MaxSummaryTokens); err != nil {
				return err
			}
		case "max_summary_calls":
			if err := applyOptionalInt(raw, &target.MaxSummaryCalls); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown history compaction setting %q", key)
		}
	}
	return nil
}

func applyContextOverridePatch(target *config.ContextBudgetConfig, request contextBudgetOverrideRequest) error {
	providerID, modelID := strings.TrimSpace(request.ProviderID), strings.TrimSpace(request.ModelID)
	if providerID == "" || modelID == "" {
		return errors.New("override requires provider_id and model_id")
	}
	index := -1
	for i, override := range target.Overrides {
		if strings.EqualFold(override.ProviderID, providerID) && strings.EqualFold(override.ModelID, modelID) {
			index = i
			break
		}
	}
	if index < 0 {
		target.Overrides = append(target.Overrides, config.ContextBudgetOverride{ProviderID: providerID, ModelID: modelID})
		index = len(target.Overrides) - 1
	}
	item := &target.Overrides[index]
	if err := applyOptionalInt(request.Context, &item.ContextTokens); err != nil {
		return err
	}
	if err := applyOptionalInt(request.Input, &item.InputTokens); err != nil {
		return err
	}
	if err := applyOptionalInt(request.Output, &item.OutputTokens); err != nil {
		return err
	}
	if item.ContextTokens == nil && item.InputTokens == nil && item.OutputTokens == nil {
		target.Overrides = append(target.Overrides[:index], target.Overrides[index+1:]...)
	}
	return nil
}

func resolveRPCContextBudget(providerID, model string, cfg config.Config) (contextbudget.Budget, error) {
	if providerID == "" {
		providerID = "native"
	}
	baseURL := ""
	if providerID != "native" {
		provider, err := resolveRPCProvider(providerID)
		if err != nil {
			return contextbudget.Budget{}, err
		}
		baseURL = provider.BaseURL
	}
	return resolveConfiguredContextBudget(providerID, model, baseURL, cfg.ContextBudget)
}

func contextBudgetPayload(budget contextbudget.Budget, cfg config.Config, baseURL string) map[string]any {
	data, _ := json.Marshal(budget)
	payload := map[string]any{}
	_ = json.Unmarshal(data, &payload)
	contextConfig := cfg.ContextBudget.Normalized()
	payload["history_compaction"] = cfg.HistoryCompaction.Normalized()
	payload["overrides"] = contextConfig.Overrides
	payload["unknown_input_budget_tokens"] = contextConfig.UnknownInputBudgetTokens
	found, stale, fetchedAt := contextBudgetCatalogState(budget.ProviderID, budget.ModelID, baseURL, time.Now())
	payload["catalog_limits"] = map[string]any{"found": found, "stale": stale, "fetched_at": nullableTime(fetchedAt)}
	return payload
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func rpcContextBudgetBaseURL(providerID string) string {
	if providerID == "" || providerID == "native" {
		return ""
	}
	provider, err := resolveRPCProvider(providerID)
	if err != nil {
		return ""
	}
	return provider.BaseURL
}

func (s *rpcServer) startManualContextCompaction(requestID string) {
	s.mu.Lock()
	if !s.started || s.session == "" {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "attach or start a session before compacting context", "recoverable": true})
		return
	}
	if s.active || s.skillOperationActive || s.pluginCommandActive || s.releaseActive || s.attachedTask != "" || s.reloadPrepared {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "context can only be compacted while the foreground session is idle", "recoverable": true})
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	options := s.opts
	providerID := s.providerID
	pluginPaths := append([]string(nil), s.pluginPaths...)
	contextConfig, historyConfig := s.contextBudgetConfig, s.historyCompactionConfig
	s.skillOperationActive, s.skillOperationKind = true, "compact"
	s.skillOperationCancel, s.skillOperationDone = cancel, make(chan struct{})
	done := s.skillOperationDone
	s.mu.Unlock()
	_ = s.emit(requestID, "compact_started", map[string]any{"session_id": options.SessionID})
	go func() {
		defer close(done)
		defer cancel()
		finishOperation := func() {
			s.mu.Lock()
			if s.skillOperationKind == "compact" && s.skillOperationDone == done {
				s.skillOperationActive, s.skillOperationCancel, s.skillOperationDone, s.skillOperationKind = false, nil, nil, ""
			}
			s.mu.Unlock()
		}
		defer finishOperation()
		client, providerSnapshot, err := s.compactionAdapter(ctx, providerID, options)
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "compact_failed", map[string]any{"message": err.Error()})
			return
		}
		if closer, ok := client.(interface{ Close() error }); ok {
			defer closer.Close()
		}
		options.Adapter = client
		options.ProviderFingerprint = ""
		if providerSnapshot != nil {
			options.ProviderFingerprint = providerSnapshot.Fingerprint()
		}
		broker := interaction.NewBroker(ctx, options.SessionID)
		defer broker.Close()
		extra := []cliRegistryExtension{tinyFishRegistryExtension(ctx)}
		if options.ImageGenFingerprint != "" {
			imageConfig := imagegen.Config{Driver: options.ImageGenFingerprint, Effort: "low", CodexHome: strings.TrimSpace(os.Getenv("CODEX_HOME"))}
			extra = append(extra, cliRegistryExtension{Decorate: imagegen.Decorator(imageConfig, options.Workspace), RemoteJobHandlers: imagegen.HandlerFactory(imageConfig, options.Workspace)})
		}
		extensionHost, err := configureCLIExtensions(ctx, &options, pluginPaths, nil, s.diagnostics,
			append(extra, cliRegistryExtension{Decorate: func(base tool.Registry) tool.Registry { return interaction.DecorateRegistry(base, broker) }})...)
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "compact_failed", map[string]any{"message": "load session extensions: " + err.Error()})
			return
		}
		if extensionHost != nil {
			defer extensionHost.Close()
		}
		finishPlugins, err := prepareRPCPluginSession(&options, extensionHost)
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "compact_failed", map[string]any{"message": "validate saved session extensions: " + err.Error()})
			return
		}
		// MCP/extension providers are needed to rebuild frozen tool schemas and
		// translate saved tool results. Their workers are closed before the
		// manual compaction request returns.
		mcpServers, err := configuredSubagentMCPServers()
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "compact_failed", map[string]any{"message": "load configured MCP servers: " + err.Error()})
			return
		}
		mcpHost, err := configureCLIMCP(ctx, &options, s.diagnostics)
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "compact_failed", map[string]any{"message": "load configured MCP servers: " + err.Error()})
			return
		}
		if mcpHost != nil {
			defer mcpHost.Close()
		}
		if providerSnapshot != nil {
			options.ProviderID = providerSnapshot.ID
		}
		subagentManager, err := configureSubagents(ctx, &options, subagentRuntimeConfig{
			Workspace: options.Workspace, SessionDir: options.SessionDir, SkillsDirs: options.SkillsDirs,
			ProviderID: options.ProviderID, ProviderConfig: providerSnapshot, Effort: options.Effort,
			ContextBudgetConfig: contextConfig, HistoryCompactionConfig: historyConfig,
			MCPServers: mcpServers, InheritMCP: len(mcpServers) > 0, Diagnostics: s.diagnostics,
		})
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "compact_failed", map[string]any{"message": "configure saved session tools: " + err.Error()})
			return
		}
		defer subagentManager.Close()
		options.OnContextCompaction = func(event runner.ContextCompactionEvent) {
			_ = s.emit(requestID, "compact_progress", map[string]any{"context_compaction": event})
		}
		result, err := runner.CompactSession(ctx, options)
		if finishErr := finishPlugins(); err == nil && finishErr != nil {
			err = finishErr
		}
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "compact_failed", map[string]any{"message": err.Error()})
			return
		}
		finishOperation()
		_ = s.emit(requestID, "compact_finished", map[string]any{"session_id": options.SessionID, "compacted": result.Compacted, "reason": result.Reason, "summary_calls": result.SummaryCalls, "compacted_items": result.CompactedItems})
	}()
}

func (s *rpcServer) compactionAdapter(ctx context.Context, providerID string, options runner.Options) (llm.Adapter, *providers.Provider, error) {
	if providerID != "" {
		provider, err := resolveRPCProvider(providerID)
		if err != nil {
			return nil, nil, err
		}
		key, err := provider.APIKeyValue()
		if err != nil {
			return nil, nil, err
		}
		provider.APIKey, provider.APIKeyEnv = key, ""
		client, err := providers.NewClient(provider)
		return client, &provider, err
	}
	s.mu.Lock()
	if s.adapter != nil {
		client := s.adapter
		s.mu.Unlock()
		return client, nil, nil
	}
	useCodex, codexPath, prepare := s.useCodex, s.codexPath, s.prepareAdapter
	s.mu.Unlock()
	if prepare == nil {
		prepare = prepareAdapter
	}
	client, err := prepare(ctx, useCodex, codexPath)
	return client, nil, err
}
