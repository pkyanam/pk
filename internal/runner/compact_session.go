package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/filetools"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// CompactSession rebuilds an idle session's current model request, then writes
// a reversible history checkpoint without appending a transcript turn.
func CompactSession(ctx context.Context, options Options) (CompactionResult, error) {
	if strings.TrimSpace(options.SessionID) == "" {
		return CompactionResult{}, errors.New("a session ID is required for manual compaction")
	}
	if options.Adapter == nil {
		return CompactionResult{}, errors.New("an LLM adapter is required for manual compaction")
	}
	if strings.TrimSpace(options.SessionDir) == "" {
		return CompactionResult{}, errors.New("a session directory is required for manual compaction")
	}
	if options.Workspace == "" {
		return CompactionResult{}, errors.New("a workspace is required for manual compaction")
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return CompactionResult{}, err
	}
	options.Workspace = workspace
	lease, err := sessionlock.Acquire(options.SessionDir, options.SessionID)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("session %s: %w", options.SessionID, err)
	}
	defer lease.Release()
	store := options.Store
	if store == nil {
		store, err = localfile.New(options.SessionDir)
		if err != nil {
			return CompactionResult{}, err
		}
	}
	state, err := store.Resume(ctx, session.ID(options.SessionID))
	if err != nil {
		return CompactionResult{}, err
	}
	for _, current := range state.Operations {
		if !isTerminal(current.Status) {
			return CompactionResult{}, errors.New("cannot compact while a tool operation is active; wait for it to finish")
		}
	}
	contextStore := options.ContextSnapshots
	if contextStore == nil {
		contextStore = defaultContextSnapshotStore(options.SessionDir, options.Workspace)
	}
	snapshot, err := contextStore.LoadContext(ctx, session.ID(options.SessionID))
	if err != nil {
		return CompactionResult{}, fmt.Errorf("load session context snapshot: %w", err)
	}
	if snapshot.Workspace != options.Workspace {
		return CompactionResult{}, errors.New("session workspace does not match the requested workspace")
	}
	if err := validateProviderFingerprint(snapshot, options.ProviderFingerprint); err != nil {
		return CompactionResult{}, err
	}
	if err := validateProviderSelection(snapshot, options.ProviderID); err != nil {
		return CompactionResult{}, err
	}
	if err := validateMCPFingerprint(snapshot, options.MCPFingerprint); err != nil {
		return CompactionResult{}, err
	}
	if err := validateImageGenFingerprint(snapshot, options.ImageGenFingerprint); err != nil {
		return CompactionResult{}, err
	}
	if err := validateOutputCompactionMode(snapshot, options.CompactCapturedOutput); err != nil {
		return CompactionResult{}, err
	}
	if options.Model == "" {
		options.Model = "gpt-6-luna"
	}
	if options.Effort == "" {
		options.Effort = "medium"
	}
	operationDir := filepath.Join(options.SessionDir, "operations")
	registryOptions := ToolRegistryOptions{Workspace: options.Workspace, OperationDir: operationDir, Skills: snapshotSkills(snapshot), SkillsSet: true}
	for _, definition := range snapshot.Tools {
		if definition.Name == tool.SkillUseName {
			registryOptions.RequireSkillUse = true
			break
		}
	}
	registryFactory := options.RegistryFactory
	if registryFactory == nil {
		registryFactory = defaultRegistryFactory
	}
	registry, _, _ := registryFactory(registryOptions)
	if registry == nil {
		return CompactionResult{}, errors.New("tool registry factory returned nil")
	}
	registry = filetools.Decorator(options.Workspace)(registry)
	if options.DecorateRegistry != nil {
		registry = options.DecorateRegistry(registry)
		if registry == nil {
			return CompactionResult{}, errors.New("registry decorator returned nil")
		}
	}
	if options.CompactCapturedOutput {
		outputStore := options.OutputCompactionStore
		if outputStore == nil {
			outputStore = newLocalOutputCompactionStore(filepath.Join(operationDir, "output-compaction", historySessionHash(options.SessionID)))
		}
		registry = compactCapturedOutputRegistry(registry, nil, outputStore)
	}
	for _, definition := range snapshot.Tools {
		if _, ok := registry.Resolve(definition.Name); !ok {
			return CompactionResult{}, fmt.Errorf("session requires unavailable tool %q", definition.Name)
		}
	}
	factory := options.BuilderFactory
	if factory == nil {
		factory = func(skills []tool.Skill) contextbuilder.Builder { return contextbuilder.NewBuilder(skills...) }
	}
	builder := factory(snapshotSkills(snapshot))
	if snapshot.IdentityTemplate != "" {
		builder = identityBuilder{Builder: builder, template: snapshot.IdentityTemplate}
	}
	builder.SetModel(llm.Model{ID: options.Model, ReasoningEffort: llm.ReasoningEffort(options.Effort)})
	builder.SetSystemPrompt(snapshot.SystemPrompt)
	for _, definition := range snapshot.Tools {
		builder.AddTool(definition)
	}
	if err := rebuildBuilder(ctx, store, session.ID(options.SessionID), builder, registry, state.Operations); err != nil {
		return CompactionResult{}, err
	}
	built, err := builder.Build()
	if err != nil {
		return CompactionResult{}, err
	}
	checkpointStore := options.HistoryCheckpointStore
	if checkpointStore == nil {
		checkpointStore = NewLocalHistoryCheckpointStore(options.SessionDir)
	}
	compactionUsageStore := options.HistoryCompactionUsageStore
	if compactionUsageStore == nil {
		compactionDir := options.SessionDir
		if compactionDir == "" {
			compactionDir = filepath.Join(options.Workspace, ".pk", "contexts")
		}
		compactionUsageStore = NewLocalHistoryCompactionUsageStore(compactionDir)
	}
	engine := historyCompactionAdapter{
		next: options.Adapter, summarizer: options.Adapter, store: checkpointStore,
		usageStore: compactionUsageStore,
		sessionID:  options.SessionID, budget: options.ContextBudget,
		policy: normalizeHistoryCompactionOptions(options.HistoryCompaction), observer: options.OnContextCompaction,
	}
	_, result, err := engine.compact(ctx, built.Request, true)
	return result, err
}

func rebuildBuilder(ctx context.Context, store sessionstore.Store, id session.ID, builder contextbuilder.Builder, registry tool.Registry, restoredOperations []operation.Operation) error {
	after := sessionstore.BeforeFirst
	turnID := session.TurnID("")
	turnType := session.TurnRegular
	type replayCall struct {
		name       string
		operations map[operation.ID]bool
	}
	toolCalls := map[string]replayCall{}
	operations := map[operation.ID]operation.Operation{}
	for _, value := range restoredOperations {
		operations[value.ID] = value
	}
	key := func(turn session.TurnID, call string) string { return string(turn) + "\x00" + call }
	for {
		page, err := store.Items(ctx, id, after, 256)
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			switch item.Kind {
			case sessionstore.ItemFork:
				return errors.New("manual compaction is not supported for forked sessions yet")
			case sessionstore.ItemInput:
				input, ok := item.Data.(inbox.Input)
				if !ok {
					return errors.New("invalid saved input")
				}
				if input.Kind == inbox.InputExternal {
					if err := builder.AddExternalInput(input); err != nil {
						return err
					}
				}
				if input.Kind == inbox.InputControl {
					control, err := input.DecodeControlMessage()
					if err != nil {
						return err
					}
					builder.AddControlMessage(control)
				}
			case sessionstore.ItemTurn:
				turn, ok := item.Data.(session.Turn)
				if !ok {
					return errors.New("invalid saved turn")
				}
				turnID, turnType = turn.ID, turn.Type
				builder.Commit()
			case sessionstore.ItemModelResponse:
				modelResponse, ok := item.Data.(sessionstore.ModelResponse)
				if !ok {
					return errors.New("invalid saved model response")
				}
				if turnType != session.TurnCompaction || modelResponse.TurnID != turnID {
					builder.AddModelResponse(modelResponse.Response)
					for _, output := range modelResponse.Response.Output {
						if output.Type == llm.ItemToolCall {
							call, ok := output.Data.(llm.ToolCall)
							if ok {
								toolCalls[key(modelResponse.TurnID, call.CallID)] = replayCall{name: call.Name, operations: make(map[operation.ID]bool)}
							}
						}
					}
				}
			case sessionstore.ItemToolCallStatus:
				status, ok := item.Data.(sessionstore.ToolCallStatus)
				if !ok {
					return errors.New("invalid saved tool status")
				}
				callKey := key(status.TurnID, status.CallID)
				call, exists := toolCalls[callKey]
				if !exists {
					continue
				}
				for _, value := range status.Operations {
					operations[value.ID] = value
				}
				for _, id := range status.Status.WaitingFor {
					call.operations[id] = true
				}
				toolCalls[callKey] = call
				translator, found := registry.Resolve(call.name)
				if !found && status.Status.Error != "" && len(status.Status.WaitingFor) == 0 && len(status.Operations) == 0 {
					builder.AddToolResult(status.CallID, []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: status.Status.Error}}, false)
					delete(toolCalls, callKey)
					continue
				}
				if !found {
					return fmt.Errorf("tool %q needed to rebuild history is unavailable", call.name)
				}
				callOperations := make([]operation.Operation, 0, len(status.Status.WaitingFor))
				for _, id := range status.Status.WaitingFor {
					if !call.operations[id] {
						continue
					}
					if value, found := operations[id]; found {
						callOperations = append(callOperations, value)
					}
				}
				result, err := translator.TranslateResult(status.CallID, status.Status, callOperations)
				if err != nil {
					return fmt.Errorf("rebuild saved tool result: %w", err)
				}
				running := false
				for id := range call.operations {
					if value, found := operations[id]; !found || !isTerminal(value.Status) {
						running = true
						break
					}
				}
				builder.AddToolResult(status.CallID, result.Output, running)
				if !running {
					delete(toolCalls, callKey)
				}
			}
		}
		if !page.More {
			return nil
		}
		if page.NextAfter <= after {
			return errors.New("session history cursor did not advance")
		}
		after = page.NextAfter
	}
}

func historySessionHash(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])
}
