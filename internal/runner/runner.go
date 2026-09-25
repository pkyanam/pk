// Package runner composes the public Unreal Agent runtime into a single-user
// terminal session. The coordinator remains responsible for scheduling,
// operation persistence, recovery, and cancellation.
package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pkyanam/pk/internal/benchcontext"
	"github.com/pkyanam/pk/internal/contextbudget"
	"github.com/pkyanam/pk/internal/filetools"
	"github.com/pkyanam/pk/internal/imagehint"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/coordinator"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
	"github.com/unreallabsai/unreal-agent/harness/tool/bash"
	"github.com/unreallabsai/unreal-agent/harness/tool/viewimage"
)

// Options describes one prompt turn. An empty SessionID creates a session; a
// non-empty ID resumes that durable session and appends Prompt as a new input.
type Options struct {
	Prompt string
	// PreallocatedNewID reserves the identity of a fresh session before the
	// first input is prepared. It is never interpreted as a resume ID.
	PreallocatedNewID string
	// PromptID is a caller-owned stable ID for the initial prompt. It lets a
	// task host retry a worker after a crash without appending the prompt twice.
	PromptID string
	// BeforeInputPersist runs after the session and input IDs are known but
	// before the initial external input is appended to the durable store.
	// Callers may persist presentation-only metadata associated with those IDs.
	BeforeInputPersist func(sessionID, inputID string) error
	// AfterInputPersist runs only after the initial external input is durably
	// appended. Callers may use it to retain input-owned artifacts.
	AfterInputPersist func(sessionID, inputID string)
	SessionID         string
	SessionDir        string
	Workspace         string
	Model             string
	Effort            string
	SystemPrompt      string
	SkillsDirs        []string
	JSONL             bool
	ToolEvents        bool
	Output            io.Writer
	Diagnostics       io.Writer
	OnSession         func(string)
	// LifecycleObserver receives metadata-only run and persisted-response
	// notifications. Implementations must be nonblocking; it is invoked on the
	// coordinator observer path.
	LifecycleObserver func(LifecycleEvent)
	Store             sessionstore.Store
	Adapter           llm.Adapter
	// Inputs, when non-nil, keeps the coordinator alive and submits each prompt
	// until the channel is closed. This is useful for task hosts that need to
	// steer a running session without rebuilding its coordinator.
	Inputs <-chan Input
	// KeepAlive keeps this run open after an assistant response so later Inputs
	// can continue the same coordinator. The default returns at assistant idle.
	KeepAlive bool
	// QueueInputs opts into boundary-safe, text-only interactive steering. The
	// runner holds new inputs while a model response or its tools are active, then
	// persists them into the coordinator inbox after the boundary. The run stays
	// available after an assistant response until the stream closes. Closing the
	// stream asks the coordinator to stop once current work settles. This does
	// not cancel active tools; cancellation remains tied to ctx. The queue is
	// bounded to 64 inputs; rejected entries receive an Accepted error.
	QueueInputs bool
	// ContextSnapshots can persist prefix snapshots for custom session stores.
	// Local sessions use a private sidecar store when this is nil.
	ContextSnapshots ContextSnapshotStore
	// ContextUsageStore persists content-free request composition measurements
	// for custom session stores. Local sessions use a private sidecar by default.
	ContextUsageStore ContextUsageStore
	// OnContextUsage receives content-free request estimates before and after
	// main provider calls. Estimates are heuristic, never published capacities.
	OnContextUsage func(ContextUsageRecord)
	// ContextBudget keeps provider/model capacities distinct from the
	// operational input allowance. History compaction never treats the
	// operational fallback as a published model limit.
	ContextBudget contextbudget.Budget
	// HistoryCompaction controls reversible, model-visible history projection.
	// The durable session transcript is never rewritten.
	HistoryCompaction      HistoryCompactionOptions
	OnContextCompaction    func(ContextCompactionEvent)
	HistoryCheckpointStore HistoryCheckpointStore
	// HistoryCompactionUsageStore records summary-provider calls separately
	// from the latest ordinary request metrics.
	HistoryCompactionUsageStore HistoryCompactionUsageStore
	// BuilderFactory and RegistryFactory allow hosts to provide extension seams
	// without changing the coordinator or the pinned upstream harness.
	BuilderFactory  func([]tool.Skill) contextbuilder.Builder
	RegistryFactory func(ToolRegistryOptions) (tool.Registry, []tool.Skill, []error)
	// DecorateRegistry adds host-specific translators to the default (or injected)
	// registry. On resume, saved tool definitions still determine the model-visible
	// schema, so adding a decorator does not silently change an existing prefix.
	DecorateRegistry func(tool.Registry) tool.Registry
	// RemoteJobHandlers creates run-scoped local remote-job handlers. The factory
	// receives the operation manager's cancellation context so handlers stop when
	// this runner has settled and tears down the operation manager.
	RemoteJobHandlers func(context.Context) []operation.RemoteJobHandler
	CaptureLimit      int
	// WorkspaceJournalRoot opts file tools into durable change journaling under
	// the given root (normally PK_HOME/journal). Empty disables journaling and
	// WorkspaceDelta. The session ID keys journal entries; file-tool coverage
	// only, never a sandbox or completeness claim.
	WorkspaceJournalRoot string
	// WorkspaceJournalLimits bounds per-session retention and object-store
	// growth. Zero selects package defaults.
	WorkspaceJournalLimits workspacejournal.Limits
	// MCPFingerprint binds an MCP tool schema/configuration set to the saved
	// session prefix. Resumes must provide the same fingerprint.
	MCPFingerprint string
	// CompactCapturedOutput shortens large completed Bash results at ingestion
	// when every non-empty output stream has a readable capture artifact. It is
	// opt-in and affects only newly translated results; saved prefixes are never
	// rewritten. OnOutputCompaction reports the actual decision for each eligible
	// result, including capture fallback.
	CompactCapturedOutput bool
	OnOutputCompaction    func(OutputCompactionEvent)
	OutputCompactionStore OutputCompactionStore
	// ProviderFingerprint binds a session to its configured API endpoint and
	// protocol. Empty preserves legacy/default native-provider sessions.
	ProviderFingerprint string
	// ProviderID is persisted to resolve the exact configured provider on attach.
	ProviderID string
	// ImageGenFingerprint freezes the separately configured ImageGen driver for
	// sessions whose saved schema contains ImageGen. Empty keeps sessions without
	// that opt-in compatible with the ordinary registry.
	ImageGenFingerprint string
}

// Input is a steer/follow-up prompt submitted to the active coordinator.
// Supplying a stable ID lets the inbox deduplicate it against restored history.
// Accepted runs after the prompt has been persisted in the session store.
type Input struct {
	ID       string
	Text     string
	Accepted func(error)
}

type ToolRegistryOptions struct {
	Workspace    string
	OperationDir string
	SkillsDirs   []string
	Skills       []tool.Skill
	SkillsSet    bool
	// RequireSkillUse preserves the tool declaration captured by older or
	// custom session snapshots even when their saved skill set is empty.
	RequireSkillUse bool
}

// RunResult reports the durable session ID and any final assistant text emitted.
type RunResult struct {
	SessionID string
	Text      string
}

// Run executes one prompt, resuming a prior session when SessionID is set.
// Canceling ctx sends a hard-stop control to the coordinator and lets it settle
// or cancel active operations before returning.
func Run(ctx context.Context, options Options) (result RunResult, runErr error) {
	if strings.TrimSpace(options.Prompt) == "" {
		return RunResult{}, errors.New("prompt must not be empty")
	}
	if options.QueueInputs && options.Inputs == nil {
		return RunResult{}, errors.New("queued steering requires an input stream")
	}
	if options.Adapter == nil {
		return RunResult{}, errors.New("LLM adapter is required")
	}
	if options.PreallocatedNewID != "" && options.SessionID != "" {
		return RunResult{}, errors.New("preallocated new-session ID is valid only when SessionID is empty")
	}
	if options.Model == "" {
		options.Model = "gpt-6-luna"
	}
	if options.Effort == "" {
		options.Effort = "medium"
	}
	effort := llm.ReasoningEffort(options.Effort)
	if !effort.Valid() {
		return RunResult{}, fmt.Errorf("unsupported reasoning effort %q", options.Effort)
	}
	if options.Workspace == "" {
		options.Workspace = "."
	}
	workspace, err := filepath.Abs(options.Workspace)
	if err != nil {
		return RunResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	options.Workspace = workspace
	workspaceInfo, err := os.Stat(options.Workspace)
	if err != nil {
		return RunResult{}, fmt.Errorf("inspect workspace: %w", err)
	}
	if !workspaceInfo.IsDir() {
		return RunResult{}, fmt.Errorf("workspace %q is not a directory", options.Workspace)
	}
	if options.SessionDir != "" {
		absolute, err := filepath.Abs(options.SessionDir)
		if err != nil {
			return RunResult{}, fmt.Errorf("resolve session directory: %w", err)
		}
		options.SessionDir = absolute
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.Diagnostics == nil {
		options.Diagnostics = io.Discard
	}
	if options.CaptureLimit <= 0 {
		options.CaptureLimit = 256 << 10
	}
	store := options.Store
	if store == nil {
		if strings.TrimSpace(options.SessionDir) == "" {
			return RunResult{}, errors.New("session directory is required")
		}
		var err error
		store, err = localfile.New(options.SessionDir)
		if err != nil {
			return RunResult{}, fmt.Errorf("open session store: %w", err)
		}
	}

	var id session.ID
	var restored sessionstore.ResumeState
	newSession := options.SessionID == ""
	if newSession {
		if options.PreallocatedNewID != "" {
			if !validPreallocatedSessionID(options.PreallocatedNewID) {
				return RunResult{}, errors.New("invalid preallocated new-session ID")
			}
			id = session.ID(options.PreallocatedNewID)
		} else {
			newID, err := newID()
			if err != nil {
				return RunResult{}, err
			}
			id = session.ID(newID)
		}
	} else {
		id = session.ID(options.SessionID)
	}
	if newSession && options.PreallocatedNewID != "" {
		if err := checkPreallocatedSessionCollision(ctx, store, options.SessionDir, id); err != nil {
			return RunResult{}, err
		}
	}
	lockDir := options.SessionDir
	if lockDir == "" {
		lockDir = filepath.Join(options.Workspace, ".pk", "sessions")
	}
	lease, err := sessionlock.Acquire(lockDir, string(id))
	if err != nil {
		return RunResult{SessionID: string(id)}, fmt.Errorf("session %s: %w", id, err)
	}
	defer func() {
		if err := lease.Release(); err != nil {
			fmt.Fprintf(options.Diagnostics, "pk: release session lease: %v\n", err)
		}
	}()
	if newSession {
		if options.PreallocatedNewID != "" {
			if err := checkPreallocatedSessionCollision(ctx, store, options.SessionDir, id); err != nil {
				return RunResult{}, err
			}
		}
		if _, err := store.Create(ctx, id); err != nil {
			return RunResult{}, fmt.Errorf("create session: %w", err)
		}
	} else {
		var err error
		restored, err = store.Resume(ctx, id)
		if err != nil {
			return RunResult{}, fmt.Errorf("resume session %q: %w", id, err)
		}
	}
	if newSession {
		var err error
		restored, err = store.Resume(ctx, id)
		if err != nil {
			return RunResult{}, fmt.Errorf("load new session: %w", err)
		}
	}
	if options.OnSession != nil {
		options.OnSession(string(id))
	}

	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	inputs, err := inbox.New(runCtx, restored.ExternalInputIDs)
	if err != nil {
		return RunResult{}, fmt.Errorf("create session inbox: %w", err)
	}
	var remoteJobHandlers []operation.RemoteJobHandler
	if strings.TrimSpace(options.WorkspaceJournalRoot) != "" {
		store, journalErr := workspacejournal.Open(options.WorkspaceJournalRoot, options.WorkspaceJournalLimits)
		if journalErr != nil {
			return RunResult{}, fmt.Errorf("open workspace journal: %w", journalErr)
		}
		workspaceJournal := filetools.HandlerFactoryWithJournal(options.Workspace, store, string(id))
		remoteJobHandlers = append(remoteJobHandlers, workspaceJournal(runCtx)...)
	} else {
		remoteJobHandlers = append(remoteJobHandlers, filetools.HandlerFactory(options.Workspace)(runCtx)...)
	}
	if options.RemoteJobHandlers != nil {
		remoteJobHandlers = append(remoteJobHandlers, options.RemoteJobHandlers(runCtx)...)
	}
	manager := operation.NewLocalOperationManager(runCtx, remoteJobHandlers...)
	defer func() {
		stopRun()
		for range manager.Updates() {
			// Drain buffered status updates while the manager shuts down. The
			// coordinator has already returned, so no consumer remains attached.
		}
		for _, handler := range remoteJobHandlers {
			if waiter, ok := handler.(interface{ Wait() }); ok {
				waiter.Wait()
			}
		}
	}()
	operationDir := filepath.Join(options.SessionDir, "operations")
	if options.SessionDir == "" {
		operationDir = filepath.Join(options.Workspace, ".pk", "operations")
	}
	if err := os.MkdirAll(operationDir, 0o700); err != nil {
		stopRun()
		return RunResult{}, fmt.Errorf("create operation directory: %w", err)
	}
	contextStore := options.ContextSnapshots
	if contextStore == nil {
		contextStore = defaultContextSnapshotStore(options.SessionDir, options.Workspace)
	}
	var snapshot ContextSnapshot
	loadedSnapshot := false
	if !newSession {
		loaded, snapshotErr := contextStore.LoadContext(ctx, id)
		if snapshotErr != nil {
			if !isMissingContextSnapshot(snapshotErr) {
				return RunResult{SessionID: string(id)}, fmt.Errorf("load session context snapshot: %w", snapshotErr)
			}
			emptyHistory, historyErr := sessionHasNoHistory(ctx, store, id)
			if historyErr != nil {
				return RunResult{SessionID: string(id)}, fmt.Errorf("inspect session history without context snapshot: %w", historyErr)
			}
			if !emptyHistory {
				fmt.Fprintf(options.Diagnostics, "pk: warning: session %s has no saved prompt context; resuming with current workspace context.\n", id)
				if options.CompactCapturedOutput {
					return RunResult{SessionID: string(id)}, errors.New("captured-output compaction cannot be enabled while resuming a session without a saved context snapshot; start a new session")
				}
			}
		} else {
			snapshot = loaded
			loadedSnapshot = true
			if snapshot.Workspace != options.Workspace {
				return RunResult{SessionID: string(id)}, fmt.Errorf("session %s belongs to workspace %q; refusing resume from %q", id, snapshot.Workspace, options.Workspace)
			}
			if err := validateMCPFingerprint(snapshot, options.MCPFingerprint); err != nil {
				return RunResult{SessionID: string(id)}, fmt.Errorf("session %s: %w", id, err)
			}
			if err := validateProviderFingerprint(snapshot, options.ProviderFingerprint); err != nil {
				return RunResult{SessionID: string(id)}, fmt.Errorf("session %s: %w", id, err)
			}
			if err := validateProviderSelection(snapshot, options.ProviderID); err != nil {
				return RunResult{SessionID: string(id)}, fmt.Errorf("session %s: %w", id, err)
			}
			if err := validateImageGenFingerprint(snapshot, options.ImageGenFingerprint); err != nil {
				return RunResult{SessionID: string(id)}, fmt.Errorf("session %s: %w", id, err)
			}
			if err := validateOutputCompactionMode(snapshot, options.CompactCapturedOutput); err != nil {
				return RunResult{SessionID: string(id)}, fmt.Errorf("session %s: %w", id, err)
			}
			if explicit := strings.TrimSpace(options.SystemPrompt); explicit != "" && explicit != snapshot.ExplicitSystemPrompt {
				return RunResult{SessionID: string(id)}, errors.New("system prompt differs from the saved session context; start a new session to use the changed instructions")
			}
		}
	}
	registryFactory := options.RegistryFactory
	registryOptions := ToolRegistryOptions{Workspace: options.Workspace, OperationDir: operationDir}
	var skillDiscoveryWarnings []error
	if loadedSnapshot {
		registryOptions.Skills, registryOptions.SkillsSet = snapshotSkills(snapshot), true
		for _, definition := range snapshot.Tools {
			if definition.Name == tool.SkillUseName {
				registryOptions.RequireSkillUse = true
				break
			}
		}
	} else if registryFactory == nil {
		// Unreal v0.1.1's path discovery is useful, but its frontmatter parser
		// treats YAML block scalars as literal markers. Decode metadata before
		// registration so SkillUse and contextbuilder share the same names.
		discovered, warnings, discoverErr := discoverCatalogSkills(ctx, options.SkillsDirs)
		if discoverErr != nil {
			return RunResult{SessionID: string(id)}, fmt.Errorf("discover skills: %w", discoverErr)
		}
		registryOptions.Skills, registryOptions.SkillsSet = discovered, true
		for _, warning := range warnings {
			skillDiscoveryWarnings = append(skillDiscoveryWarnings, errors.New(warning))
		}
	} else {
		registryOptions.SkillsDirs = options.SkillsDirs
	}
	if registryFactory == nil {
		registryFactory = defaultRegistryFactory
	}
	registry, skills, warnings := registryFactory(registryOptions)
	if registry == nil {
		return RunResult{SessionID: string(id)}, errors.New("tool registry factory returned nil")
	}
	registry = filetools.Decorator(options.Workspace)(registry)
	if strings.TrimSpace(options.WorkspaceJournalRoot) != "" {
		registry = filetools.DeltaDecorator()(registry)
	}
	warnings = append(skillDiscoveryWarnings, warnings...)
	if options.DecorateRegistry != nil {
		registry = options.DecorateRegistry(registry)
		if registry == nil {
			return RunResult{SessionID: string(id)}, errors.New("registry decorator returned nil")
		}
	}
	if options.CompactCapturedOutput {
		compactionStore := options.OutputCompactionStore
		if compactionStore == nil {
			digest := sha256.Sum256([]byte(id))
			compactionStore = newLocalOutputCompactionStore(filepath.Join(operationDir, "output-compaction", hex.EncodeToString(digest[:])))
		}
		registry = compactCapturedOutputRegistry(registry, options.OnOutputCompaction, compactionStore)
	}
	for _, warning := range warnings {
		fmt.Fprintf(options.Diagnostics, "pk: warning: %v\n", warning)
	}
	if loadedSnapshot {
		var missingTools []string
		for _, definition := range snapshot.Tools {
			if _, ok := registry.Resolve(definition.Name); !ok {
				missingTools = append(missingTools, definition.Name)
			}
		}
		if len(missingTools) != 0 {
			return RunResult{SessionID: string(id)}, fmt.Errorf(
				"session %s requires tools unavailable in this run (%s); restore the original extension and image-driver configuration or start a new session",
				id,
				strings.Join(missingTools, ", "),
			)
		}
		captured, captureErr := captureSkills(skills)
		if captureErr != nil {
			return RunResult{SessionID: string(id)}, captureErr
		}
		if !sameContextSkills(captured, snapshot.Skills) {
			return RunResult{SessionID: string(id)}, fmt.Errorf("skills for session %s changed since its context was saved; restore the original skill files or start a new session", id)
		}
	} else {
		snapshot = newContextSnapshot(options, registry, skills)
		snapshot.IdentityTemplate = defaultIdentityTemplate
		snapshot.MCPFingerprint = options.MCPFingerprint
		snapshot.ProviderFingerprint = options.ProviderFingerprint
		snapshot.ImageGenFingerprint = options.ImageGenFingerprint
		if options.CompactCapturedOutput {
			snapshot.OutputCompactionVersion = outputDecisionVersion
		}
		captured, captureErr := captureSkills(skills)
		if captureErr != nil {
			return RunResult{SessionID: string(id)}, captureErr
		}
		snapshot.Skills = captured
		if err := contextStore.SaveContext(ctx, id, snapshot); err != nil {
			return RunResult{SessionID: string(id)}, fmt.Errorf("save session context snapshot: %w", err)
		}
	}
	builderFactory := options.BuilderFactory
	if builderFactory == nil {
		builderFactory = func(skills []tool.Skill) contextbuilder.Builder {
			return contextbuilder.NewBuilder(skills...)
		}
	}
	builder := builderFactory(skills)
	if builder == nil {
		return RunResult{SessionID: string(id)}, errors.New("builder factory returned nil")
	}
	if snapshot.IdentityTemplate != "" {
		builder = identityBuilder{Builder: builder, template: snapshot.IdentityTemplate}
	}
	builder.SetModel(llm.Model{ID: options.Model, ReasoningEffort: effort})
	builder.SetSystemPrompt(snapshot.SystemPrompt)
	for _, definition := range snapshot.Tools {
		builder.AddTool(definition)
	}
	contextUsageStore := options.ContextUsageStore
	if contextUsageStore == nil {
		contextUsageStore = defaultContextUsageStore(options.SessionDir, options.Workspace)
	}
	contextUsageOrdinal := 0
	if previous, loadErr := contextUsageStore.LoadContextUsage(ctx, id); loadErr == nil {
		contextUsageOrdinal = previous.RequestOrdinal
	}
	providerAdapter := options.Adapter
	responseTimings := &responseTimingStore{}
	usageAdapter := &contextUsageAdapter{
		next: options.Adapter, store: contextUsageStore, id: id,
		ordinal: contextUsageOrdinal, timings: responseTimings,
		budget: options.ContextBudget, policy: options.HistoryCompaction, onUpdate: options.OnContextUsage,
		onError: func(err error) { fmt.Fprintf(options.Diagnostics, "pk: warning: %v\n", err) },
	}
	options.Adapter = usageAdapter
	checkpointStore := options.HistoryCheckpointStore
	if checkpointStore == nil {
		checkpointDir := options.SessionDir
		if checkpointDir == "" {
			checkpointDir = filepath.Join(options.Workspace, ".pk", "contexts")
		}
		checkpointStore = NewLocalHistoryCheckpointStore(checkpointDir)
	}
	compactionUsageStore := options.HistoryCompactionUsageStore
	if compactionUsageStore == nil {
		compactionDir := options.SessionDir
		if compactionDir == "" {
			compactionDir = filepath.Join(options.Workspace, ".pk", "contexts")
		}
		compactionUsageStore = NewLocalHistoryCompactionUsageStore(compactionDir)
	}
	options.Adapter = &historyCompactionAdapter{
		next: usageAdapter, summarizer: providerAdapter, store: checkpointStore,
		usageStore: compactionUsageStore,
		sessionID:  string(id), budget: options.ContextBudget,
		policy: normalizeHistoryCompactionOptions(options.HistoryCompaction), observer: options.OnContextCompaction,
	}
	lifecycle := func(eventType, status string) {}
	if options.LifecycleObserver != nil {
		runID, err := newID()
		if err != nil {
			return RunResult{SessionID: string(id)}, fmt.Errorf("generate lifecycle run ID: %w", err)
		}
		lifecycle = func(eventType, status string) {
			options.LifecycleObserver(LifecycleEvent{Type: eventType, RunID: runID, SessionID: string(id), Model: options.Model, Workspace: options.Workspace, Status: status})
		}
		lifecycle(LifecycleRunStart, "running")
		defer func() {
			status := "completed"
			if runErr != nil {
				if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
					status = "cancelled"
				} else {
					status = "failed"
				}
			}
			lifecycle(LifecycleRunEnd, status)
		}()
	}
	var emitted strings.Builder
	var emittedMu sync.Mutex
	var inputGate *inputBoundaryGate
	if options.QueueInputs {
		inputGate = newInputBoundaryGate()
	}
	var inputAckMu sync.Mutex
	inputAcks := make(map[inbox.ID][]func(error))
	inputAcked := make(map[inbox.ID]struct{}, len(restored.ExternalInputIDs))
	for _, inputID := range restored.ExternalInputIDs {
		inputAcked[inputID] = struct{}{}
	}
	ackPendingInputs := func(err error) {
		inputAckMu.Lock()
		callbacks := make([]func(error), 0, len(inputAcks))
		for id, current := range inputAcks {
			delete(inputAcks, id)
			callbacks = append(callbacks, current...)
		}
		inputAckMu.Unlock()
		for _, callback := range callbacks {
			callback(err)
		}
	}
	defer ackPendingInputs(context.Canceled)
	var outputErr error
	var outputErrMu sync.Mutex
	recordOutputErr := func(err error) {
		if err == nil {
			return
		}
		outputErrMu.Lock()
		if outputErr == nil {
			outputErr = err
		}
		outputErrMu.Unlock()
	}
	if options.JSONL {
		recordOutputErr(writeJSONLine(options.Output, map[string]any{"type": "session", "session_id": id}))
		recordOutputErr(writeJSONLine(options.Output, map[string]any{"type": "model", "session_id": id, "model": options.Model, "effort": options.Effort}))
	}
	interactiveInputs := options.KeepAlive || options.QueueInputs
	// QueueInputs only governs how steering is admitted at model/tool
	// boundaries. It must not turn a foreground prompt into an indefinitely
	// live run: only KeepAlive suppresses the assistant-idle stop.
	observer := outputObserver(options.Output, options.Diagnostics, options.JSONL, options.ToolEvents, runCtx, inputs, &emitted, &emittedMu, options.CaptureLimit, !options.KeepAlive, inputAcks, inputAcked, &inputAckMu, inputGate, responseTimings, recordOutputErr, func() { lifecycle(LifecycleResponseComplete, "persisted") })
	observerID := store.AddObserver(observer)
	defer store.RemoveObserver(observerID)
	current := coordinator.New(coordinator.Dependencies{
		SessionID: id, Inbox: inputs, Restored: restored, Sessions: store,
		ContextBuilder: builder, LLM: options.Adapter, Tools: registry,
		Operations: manager,
	})
	done := make(chan error, 1)
	go func() { done <- current.Run(runCtx) }()
	inputID := strings.TrimSpace(options.PromptID)
	if inputID == "" {
		inputID, err = newID()
		if err != nil {
			stopRun()
			<-done
			return RunResult{}, err
		}
	}
	payload, err := json.Marshal(options.Prompt)
	if err != nil {
		stopRun()
		<-done
		return RunResult{}, fmt.Errorf("encode prompt: %w", err)
	}
	if options.BeforeInputPersist != nil {
		if err := options.BeforeInputPersist(string(id), inputID); err != nil {
			stopRun()
			<-done
			return RunResult{SessionID: string(id)}, fmt.Errorf("persist input metadata: %w", err)
		}
	}
	if err := inputs.Submit(ctx, inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: payload}); err != nil {
		stopRun()
		<-done
		return RunResult{}, fmt.Errorf("submit prompt: %w", err)
	}
	if options.AfterInputPersist != nil {
		options.AfterInputPersist(string(id), inputID)
	}
	if options.Inputs != nil {
		go pumpInputs(runCtx, inputs, options.Inputs, interactiveInputs, inputGate, builder, store, id, stopRun, inputAcks, inputAcked, &inputAckMu, recordOutputErr)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			return RunResult{SessionID: string(id)}, fmt.Errorf("run session: %w", err)
		}
	case <-ctx.Done():
		stopSubmitted := make(chan error, 1)
		go func() { stopSubmitted <- submitStop(runCtx, inputs, inbox.StopHard, "interrupted") }()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				return RunResult{SessionID: string(id)}, fmt.Errorf("run session: %w", err)
			}
		case err := <-stopSubmitted:
			if err != nil {
				return RunResult{SessionID: string(id)}, fmt.Errorf("stop session: %w", err)
			}
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				return RunResult{SessionID: string(id)}, fmt.Errorf("run session: %w", err)
			}
		}
		emittedMu.Lock()
		text := emitted.String()
		emittedMu.Unlock()
		return RunResult{SessionID: string(id), Text: text}, ctx.Err()
	}
	emittedMu.Lock()
	text := emitted.String()
	emittedMu.Unlock()
	outputErrMu.Lock()
	err = outputErr
	outputErrMu.Unlock()
	if err != nil {
		return RunResult{SessionID: string(id), Text: text}, fmt.Errorf("write output: %w", err)
	}
	return RunResult{SessionID: string(id), Text: text}, nil
}

func checkPreallocatedSessionCollision(ctx context.Context, store sessionstore.Store, sessionDir string, id session.ID) error {
	if sessionDir != "" {
		if _, statErr := os.Lstat(filepath.Join(sessionDir, string(id)+".session.jsonl")); statErr == nil {
			return fmt.Errorf("preallocated new-session ID %q already exists", id)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("check preallocated session ID: %w", statErr)
		}
	}
	if _, resumeErr := store.Resume(ctx, id); resumeErr == nil {
		return fmt.Errorf("preallocated new-session ID %q already exists", id)
	}
	return nil
}

func outputObserver(out, diagnostics io.Writer, jsonl, toolEvents bool, runCtx context.Context, inputs *inbox.Inbox, emitted *strings.Builder, emittedMu *sync.Mutex, captureLimit int, stopWhenIdle bool, inputAcks map[inbox.ID][]func(error), inputAcked map[inbox.ID]struct{}, inputAckMu *sync.Mutex, inputGate *inputBoundaryGate, responseTimings *responseTimingStore, recordOutputErr func(error), onResponseComplete func()) sessionstore.Observer {
	var mu sync.Mutex
	toolCalls := make(map[string]toolCallMetadata)
	pendingTools := make(map[string]bool)
	sawToolWork, recoveredProgress := false, false
	return func(id session.ID, item sessionstore.Item) {
		if item.Kind == sessionstore.ItemInput {
			input, ok := item.Data.(inbox.Input)
			if !ok {
				return
			}
			inputAckMu.Lock()
			callbacks := inputAcks[input.ID]
			delete(inputAcks, input.ID)
			inputAcked[input.ID] = struct{}{}
			inputAckMu.Unlock()
			for _, callback := range callbacks {
				callback(nil)
			}
			return
		} else if item.Kind == sessionstore.ItemModelResponse {
			response := item.Data.(sessionstore.ModelResponse).Response
			if onResponseComplete != nil {
				onResponseComplete()
			}
			if inputGate != nil {
				callIDs := make([]string, 0)
				for _, output := range response.Output {
					if output.Type == llm.ItemToolCall {
						callIDs = append(callIDs, output.Data.(llm.ToolCall).CallID)
					}
				}
				inputGate.modelResponse(runCtx, callIDs)
			}
			if jsonl {
				duration, _ := responseTimings.Take(response.ID)
				if event := usageJSONEvent(id, response, duration); event != nil {
					recordOutputErr(emitJSONLine(&mu, out, event))
				}
			}
			for _, output := range response.Output {
				if output.Type == llm.ItemToolCall {
					call := output.Data.(llm.ToolCall)
					mu.Lock()
					if _, exists := toolCalls[call.CallID]; !exists {
						toolCalls[call.CallID] = toolCallMetadata{Name: call.Name, Arguments: call.Arguments, StartedAt: time.Now()}
						pendingTools[call.CallID] = true
					}
					mu.Unlock()
				}
			}
		} else if item.Kind == sessionstore.ItemToolCallStatus {
			status := item.Data.(sessionstore.ToolCallStatus)
			if inputGate != nil && toolState(status.Status, status.Operations) != "running" {
				inputGate.toolSettled(runCtx, status.CallID)
			}
			mu.Lock()
			metadata := toolCalls[status.CallID]
			if toolState(status.Status, status.Operations) != "running" {
				delete(pendingTools, status.CallID)
			}
			mu.Unlock()
			name := metadata.Name
			if toolEvents {
				if name == "" {
					name = "tool"
				}
				message := "Finished " + name
				if status.Status.Error != "" {
					message = name + " failed: " + status.Status.Error
				} else {
					for _, current := range status.Operations {
						if !isTerminal(current.Status) {
							message = "Running " + name
							break
						}
					}
				}
				mu.Lock()
				_, err := fmt.Fprintf(diagnostics, "[pk] %s.\n", message)
				mu.Unlock()
				recordOutputErr(err)
			}
			if jsonl {
				if name == "" {
					name = "tool"
				}
				recordOutputErr(emitJSONLine(&mu, out, toolCallJSONEvent(id, status, metadata, name)))
			}
			return
		} else {
			return
		}
		response := item.Data.(sessionstore.ModelResponse).Response
		type assistantText struct{ text, phase string }
		var messages []assistantText
		hasToolCall := false
		for _, output := range response.Output {
			switch output.Type {
			case llm.ItemMessage:
				message, ok := output.Data.(llm.Message)
				if ok && message.Role == llm.RoleAssistant && message.Phase != "analysis" && message.Text != "" {
					messages = append(messages, assistantText{text: message.Text, phase: message.Phase})
				}
			case llm.ItemToolCall:
				hasToolCall = true
			}
		}
		for _, message := range messages {
			appendCaptured(emitted, emittedMu, message.text+"\n", captureLimit)
			mu.Lock()
			if jsonl {
				recordOutputErr(writeJSONLine(out, map[string]any{"type": "assistant", "session_id": id, "text": message.text, "phase": message.phase, "response_id": response.ID}))
			} else {
				_, err := fmt.Fprintln(out, message.text)
				recordOutputErr(err)
			}
			mu.Unlock()
		}
		if hasToolCall {
			sawToolWork = true
		}
		mu.Lock()
		hasPendingTools := len(pendingTools) > 0
		mu.Unlock()
		if !hasToolCall && !hasPendingTools && stopWhenIdle && sawToolWork && !recoveredProgress && response.Stop == llm.StopComplete && len(messages) == 1 && unfinishedProgress(messages[0].text, messages[0].phase) && (inputGate == nil || !inputGate.waiting()) {
			recoveredProgress = true
			// Persist the labeled recovery input through the ordinary inbox, outside
			// the synchronous store observer. Never loop indefinitely on prose.
			go func() {
				inputID, err := newID()
				if err == nil {
					payload, encodeErr := json.Marshal(progressRecoveryNote)
					err = encodeErr
					if err == nil {
						err = inputs.Submit(runCtx, inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: payload})
					}
				}
				if err != nil {
					recordOutputErr(err)
					_ = submitStop(runCtx, inputs, inbox.StopWhenIdle, "progress recovery failed")
				}
			}()
			return
		}
		if !hasToolCall && stopWhenIdle {
			// Submit outside the observer: store callbacks are synchronous while the
			// coordinator is processing this event, so a direct Submit would deadlock.
			go func() { _ = submitStop(runCtx, inputs, inbox.StopWhenIdle, "assistant turn complete") }()
		}
	}
}

func toolState(status tool.CallStatus, operations []operation.Operation) string {
	if status.Error != "" {
		return "failed"
	}
	for _, current := range operations {
		switch current.Status {
		case operation.StatusCanceled:
			return "canceled"
		case operation.StatusFailed:
			return "failed"
		case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
			return "running"
		}
	}
	if len(operations) == 0 && len(status.WaitingFor) > 0 {
		return "running"
	}
	return "completed"
}

type toolCallMetadata struct {
	Name      string
	Arguments string
	StartedAt time.Time
}

const previewLimit = 240

var sensitiveAssignmentPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|credential)\b\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s;&|]+)`)
var sensitiveAuthorizationPattern = regexp.MustCompile(`(?i)\bauthorization\s*:\s*(?:bearer\s+)?[^'"\s]+`)

func toolCallJSONEvent(id session.ID, status sessionstore.ToolCallStatus, metadata toolCallMetadata, name string) map[string]any {
	argumentsPreview, commandPreview := toolArgumentPreviews(metadata.Arguments, name)
	operations := make([]map[string]any, 0, len(status.Operations))
	for _, current := range status.Operations {
		operations = append(operations, operationJSONEvent(current))
	}
	event := map[string]any{
		"type": "tool_call", "session_id": id, "call_id": status.CallID,
		"name": name, "state": toolState(status.Status, status.Operations), "status": status.Status,
		"operations": operations,
	}
	if argumentsPreview != "" {
		event["arguments_preview"] = argumentsPreview
	}
	if commandPreview != "" {
		event["command_preview"] = commandPreview
	}
	if !metadata.StartedAt.IsZero() {
		event["elapsed_ms"] = time.Since(metadata.StartedAt).Milliseconds()
	}
	if status.Status.Error != "" {
		event["error_excerpt"] = boundedPreview(status.Status.Error)
	}
	return event
}

func operationJSONEvent(current operation.Operation) map[string]any {
	result := map[string]any{"id": current.ID, "type": current.Type, "state": string(current.Status)}
	if current.Type == operation.TypeRemoteJob && current.Status == operation.StatusCompleted {
		if state, err := operation.DecodeRemoteJobState(current); err == nil && state.Plan.Type == "pk.imagegen" {
			var generated struct {
				Path   string `json:"path"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
				MIME   string `json:"mime"`
			}
			if json.Unmarshal([]byte(state.TerminalResult), &generated) == nil && generated.Path != "" {
				result["generated_image"] = generated
			}
		}
	}
	if current.Type != operation.TypeShell {
		return result
	}
	state, err := operation.DecodeShellState(current)
	if err != nil {
		result["error_excerpt"] = boundedPreview(err.Error())
		return result
	}
	stdout, stderr := string(state.InlineOut), string(state.InlineErr)
	if state.Result != nil {
		stdout, stderr = state.Result.Out, state.Result.Err
		result["exit_code"] = state.Result.ExitCode
	} else if state.TerminalError != "" {
		stderr = state.TerminalError
	}
	if stdout != "" {
		result["output_excerpt"] = boundedPreview(stdout)
	}
	if stderr != "" {
		result["error_excerpt"] = boundedPreview(stderr)
	}
	return result
}

func toolArgumentPreviews(arguments, name string) (argumentsPreview, commandPreview string) {
	var decoded any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return boundedPreview(redactSensitive(arguments)), ""
	}
	safe := redactJSON(decoded, "")
	encoded, err := json.Marshal(safe)
	if err != nil {
		argumentsPreview = boundedPreview(redactSensitive(arguments))
	} else {
		argumentsPreview = boundedPreview(string(encoded))
	}
	if name == tool.BashName {
		if values, ok := decoded.(map[string]any); ok {
			if command, ok := values["command"].(string); ok {
				commandPreview = boundedPreview(redactSensitive(command))
			}
		}
	}
	return argumentsPreview, commandPreview
}

func redactJSON(value any, key string) any {
	if isSensitiveKey(key) {
		return "[redacted]"
	}
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for currentKey, currentValue := range value {
			result[currentKey] = redactJSON(currentValue, currentKey)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, currentValue := range value {
			result[index] = redactJSON(currentValue, key)
		}
		return result
	case string:
		return redactSensitive(value)
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, fragment := range []string{"token", "password", "secret", "credential", "authorization", "api_key", "private_key"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

func redactSensitive(value string) string {
	value = sensitiveAssignmentPattern.ReplaceAllString(value, `${1}=[redacted]`)
	return sensitiveAuthorizationPattern.ReplaceAllString(value, "Authorization: [redacted]")
}

func boundedPreview(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= previewLimit {
		return value
	}
	cut := previewLimit - len("…")
	for cut > 0 && (value[cut]&0xc0) == 0x80 {
		cut--
	}
	return value[:cut] + "…"
}

func isTerminal(status operation.Status) bool {
	return status == operation.StatusCompleted || status == operation.StatusFailed || status == operation.StatusCanceled
}

func pumpInputs(
	ctx context.Context,
	inputs *inbox.Inbox,
	stream <-chan Input,
	keepAlive bool,
	gate *inputBoundaryGate,
	builder contextbuilder.Builder,
	store sessionstore.Store,
	sessionID session.ID,
	cancelRun context.CancelFunc,
	acks map[inbox.ID][]func(error),
	acked map[inbox.ID]struct{},
	ackMu *sync.Mutex,
	recordOutputErr func(error),
) {
	queued := make([]Input, 0)
	const maxQueuedInputs = 64
	defer func() {
		for _, incoming := range queued {
			if incoming.Accepted != nil {
				incoming.Accepted(context.Canceled)
			}
		}
	}()
	var submit func(Input) error
	submit = func(incoming Input) error {
		id := strings.TrimSpace(incoming.ID)
		if id == "" {
			var err error
			id, err = newID()
			if err != nil {
				if incoming.Accepted != nil {
					incoming.Accepted(err)
				}
				return err
			}
		}
		inputID := inbox.ID(id)
		ackMu.Lock()
		_, alreadyPersisted := acked[inputID]
		if !alreadyPersisted && incoming.Accepted != nil {
			acks[inputID] = append(acks[inputID], incoming.Accepted)
		}
		ackMu.Unlock()
		if alreadyPersisted {
			if incoming.Accepted != nil {
				incoming.Accepted(nil)
			}
			return nil
		}
		payload, err := json.Marshal(incoming.Text)
		if err == nil {
			err = inputs.Submit(ctx, inbox.Input{ID: inputID, Kind: inbox.InputExternal, Payload: payload})
		}
		if err != nil {
			ackMu.Lock()
			callbacks := acks[inputID]
			delete(acks, inputID)
			ackMu.Unlock()
			for _, callback := range callbacks {
				callback(err)
			}
			recordOutputErr(fmt.Errorf("submit follow-up input: %w", err))
		}
		return err
	}
	injectAtBoundary := func(incoming Input) error {
		id := strings.TrimSpace(incoming.ID)
		if id == "" {
			var err error
			id, err = newID()
			if err != nil {
				return err
			}
		}
		inputID := inbox.ID(id)
		ackMu.Lock()
		_, alreadyPersisted := acked[inputID]
		ackMu.Unlock()
		if alreadyPersisted {
			if incoming.Accepted != nil {
				incoming.Accepted(nil)
			}
			return nil
		}
		payload, err := json.Marshal(incoming.Text)
		if err != nil {
			return err
		}
		input := inbox.Input{ID: inputID, Kind: inbox.InputExternal, Payload: payload}
		if err := store.AppendInput(ctx, sessionID, input); err != nil {
			return fmt.Errorf("persist queued steering: %w", err)
		}
		if err := builder.AddExternalInput(input); err != nil {
			return fmt.Errorf("add queued steering to context: %w", err)
		}
		// AppendInput synchronously notifies observers, marking this ID durable;
		// acknowledge only after the bytes are also in the current request builder.
		if incoming.Accepted != nil {
			incoming.Accepted(nil)
		}
		return nil
	}
	flush := func() {
		if len(queued) == 0 {
			if gate != nil {
				gate.finishBoundary(false)
			}
			return
		}
		directInjection := gate != nil && gate.directBoundary()
		for index, incoming := range queued {
			var err error
			if directInjection {
				err = injectAtBoundary(incoming)
			} else {
				err = submit(incoming)
			}
			if err != nil {
				if directInjection && incoming.Accepted != nil {
					incoming.Accepted(err)
				}
				for _, pending := range queued[index+1:] {
					if pending.Accepted != nil {
						pending.Accepted(err)
					}
				}
				recordOutputErr(err)
				if directInjection {
					// The coordinator is waiting at a tool boundary. Do not let it build
					// a request without the durable steering entry we could not inject.
					cancelRun()
				}
				break
			}
		}
		queued = queued[:0]
		if gate != nil {
			gate.finishBoundary(true)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-gateWake(gate):
			if gate != nil && gate.boundaryReady() {
				flush()
			}
		case incoming, ok := <-stream:
			if !ok {
				if keepAlive {
					if err := submitStop(ctx, inputs, inbox.StopWhenIdle, "input stream closed"); err != nil && !errors.Is(err, context.Canceled) {
						recordOutputErr(err)
					}
				}
				if gate == nil {
					return
				}
				// Keep servicing boundary acknowledgments until the coordinator exits.
				// In particular, a terminal model response may synchronously wait for
				// queued inputs to be submitted before it can observe StopWhenIdle.
				stream = nil
				continue
			}
			if gate != nil && (gate.waiting() || gate.boundaryReady() || len(queued) != 0) {
				if len(queued) >= maxQueuedInputs {
					err := fmt.Errorf("interactive input queue is full (limit %d)", maxQueuedInputs)
					if incoming.Accepted != nil {
						incoming.Accepted(err)
					}
					recordOutputErr(err)
					continue
				}
				queued = append(queued, incoming)
				if gate.boundaryReady() {
					flush()
				}
				continue
			}
			if err := submit(incoming); err != nil {
				continue
			}
			if gate != nil {
				gate.submittedInput()
			}
		}
	}
}

func gateWake(gate *inputBoundaryGate) <-chan struct{} {
	if gate == nil {
		return nil
	}
	return gate.wake
}

// inputBoundaryGate keeps interactive prompts out of the active provider/tool
// cycle. The pinned coordinator may otherwise request another model response
// during its tool grace period when a prompt arrives mid-tool. QueueInputs
// holds such entries in the runner until every tool from the response settles.
type inputBoundaryGate struct {
	mu          sync.Mutex
	phase       inputGatePhase
	outstanding map[string]struct{}
	autoNext    bool
	readyAck    chan struct{}
	wake        chan struct{}
}

type inputGatePhase uint8

const (
	inputAwaitingModel inputGatePhase = iota
	inputAwaitingTools
	inputBoundaryOpen
	inputIdle
)

func newInputBoundaryGate() *inputBoundaryGate {
	return &inputBoundaryGate{phase: inputAwaitingModel, outstanding: make(map[string]struct{}), wake: make(chan struct{}, 1)}
}

func (gate *inputBoundaryGate) signal() {
	select {
	case gate.wake <- struct{}{}:
	default:
	}
}

func (gate *inputBoundaryGate) waiting() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.phase == inputAwaitingModel || gate.phase == inputAwaitingTools
}

func (gate *inputBoundaryGate) boundaryReady() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.phase == inputBoundaryOpen
}

func (gate *inputBoundaryGate) directBoundary() bool {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.phase == inputBoundaryOpen && gate.autoNext
}

func (gate *inputBoundaryGate) submittedInput() {
	gate.mu.Lock()
	gate.phase = inputAwaitingModel
	gate.autoNext = false
	gate.mu.Unlock()
}

func (gate *inputBoundaryGate) finishBoundary(submitted bool) {
	gate.mu.Lock()
	if submitted || gate.autoNext {
		gate.phase = inputAwaitingModel
	} else {
		gate.phase = inputIdle
	}
	gate.autoNext = false
	if gate.readyAck != nil {
		close(gate.readyAck)
		gate.readyAck = nil
	}
	gate.mu.Unlock()
}

func (gate *inputBoundaryGate) modelResponse(ctx context.Context, callIDs []string) {
	gate.mu.Lock()
	clear(gate.outstanding)
	for _, callID := range callIDs {
		gate.outstanding[callID] = struct{}{}
	}
	if len(gate.outstanding) == 0 {
		gate.phase = inputBoundaryOpen
		gate.autoNext = false
		gate.readyAck = make(chan struct{})
	} else {
		gate.phase = inputAwaitingTools
		gate.autoNext = true
	}
	ack := gate.readyAck
	gate.mu.Unlock()
	gate.signal()
	if len(callIDs) == 0 {
		awaitBoundary(ctx, ack)
	}
}

func (gate *inputBoundaryGate) toolSettled(ctx context.Context, callID string) {
	gate.mu.Lock()
	if gate.phase != inputAwaitingTools {
		gate.mu.Unlock()
		return
	}
	delete(gate.outstanding, callID)
	var ack chan struct{}
	if len(gate.outstanding) == 0 {
		gate.phase = inputBoundaryOpen
		gate.autoNext = true
		gate.readyAck = make(chan struct{})
		ack = gate.readyAck
	}
	gate.mu.Unlock()
	gate.signal()
	awaitBoundary(ctx, ack)
}

func awaitBoundary(ctx context.Context, ack <-chan struct{}) {
	if ack == nil {
		return
	}
	select {
	case <-ack:
	case <-ctx.Done():
	}
}

func appendCaptured(capture *strings.Builder, mu *sync.Mutex, text string, limit int) {
	mu.Lock()
	defer mu.Unlock()
	remaining := limit - capture.Len()
	if remaining <= 0 {
		return
	}
	if len(text) <= remaining {
		capture.WriteString(text)
		return
	}
	cut := remaining
	for cut > 0 && cut < len(text) && (text[cut]&0xc0) == 0x80 {
		cut--
	}
	capture.WriteString(text[:cut])
	capture.WriteString("\n[pk: captured output truncated; streamed output is complete]\n")
}

func sessionHasNoHistory(ctx context.Context, store sessionstore.Store, id session.ID) (bool, error) {
	page, err := store.Items(ctx, id, sessionstore.BeforeFirst, 1)
	if err != nil {
		return false, err
	}
	return len(page.Items) == 0 && !page.More, nil
}

func usageJSONEvent(id session.ID, response llm.Response, durations ...time.Duration) map[string]any {
	usage := response.Usage
	measured := benchcontext.MeasureUsage(usage)
	event := map[string]any{
		"type": "usage", "session_id": id, "response_id": response.ID,
		"input_tokens_available": measured.InputAvailable, "output_tokens_available": measured.OutputAvailable,
		"cached_input_tokens_available":      measured.CachedInputAvailable,
		"cache_write_input_tokens_available": measured.CacheWriteAvailable,
		"usage_available":                    measured.InputAvailable || measured.OutputAvailable || measured.CachedInputAvailable || measured.CacheWriteAvailable || usage.ReasoningTokens != 0,
	}
	if measured.InputAvailable {
		event["input_tokens"] = usage.InputTokens
	}
	if measured.OutputAvailable {
		event["output_tokens"] = usage.OutputTokens
	}
	var duration time.Duration
	if len(durations) > 0 && durations[0] > 0 {
		duration = durations[0]
		event["response_duration_ms"] = float64(duration) / float64(time.Millisecond)
		if measured.OutputAvailable {
			event["output_tokens_per_second"] = float64(usage.OutputTokens) / duration.Seconds()
		}
	}
	if usage.ReasoningTokens != 0 {
		event["reasoning_tokens"] = usage.ReasoningTokens
	}
	if measured.CachedInputAvailable {
		event["cached_input_tokens"] = measured.CachedInputTokens
	}
	if measured.CacheWriteAvailable {
		event["cache_write_input_tokens"] = measured.CacheWriteTokens
	}
	return event
}

const maxWorkspaceInstructions = 64 << 10

func workspaceSystemPrompt(workspace, explicit string, diagnostics io.Writer) string {
	sections := []string{
		"You are pk, a local coding agent working in the user's current project. Prefer Read for file contents, Bash for searches and checks, and WriteFile/EditFile for focused changes. Explore relevant code before changing it, keep edits focused, and report what changed and what you verified without claiming checks that did not run. For substantial multi-step tasks, give the user a brief plan before the first tool call and concise factual updates at meaningful milestones while work continues. Put progress messages alongside the tool work they describe; do not send a standalone progress-only turn that ends the work. Never use timer-based filler updates, and do not expose hidden reasoning. Continue until the requested task is done and verified, then summarize the result and checks. Ask the user when key information is missing or an action needs a choice. Do not expose credentials or other secrets. Before editing a nested path, inspect and follow the nearest nested AGENTS.md; pk automatically loads only the workspace-root AGENTS.md.\n\nWorkspace: " + workspace + "\nPlatform: " + runtime.GOOS + "/" + runtime.GOARCH + "; shell: /bin/sh.",
	}
	if probeWorkspaceGit(context.Background(), workspace) == workspaceGitNotWorkTree {
		sections = append(sections, "At session start this workspace was not inside a Git work tree. Before relying on git diff, check whether this is the intended repository; this fact may change during the session.")
	}
	agentsPath := filepath.Join(workspace, "AGENTS.md")
	info, err := os.Stat(agentsPath)
	if err == nil && !info.Mode().IsRegular() {
		fmt.Fprintf(diagnostics, "pk: warning: workspace instructions %s is not a regular file; not loaded.\n", agentsPath)
	} else if err == nil {
		file, openErr := os.Open(agentsPath)
		if openErr != nil {
			fmt.Fprintf(diagnostics, "pk: warning: cannot read workspace instructions %s: %v\n", agentsPath, openErr)
		} else {
			fileInfo, statErr := file.Stat()
			contents, readErr := io.ReadAll(io.LimitReader(file, maxWorkspaceInstructions+1))
			closeErr := file.Close()
			switch {
			case statErr != nil:
				fmt.Fprintf(diagnostics, "pk: warning: cannot inspect workspace instructions %s: %v\n", agentsPath, statErr)
			case !fileInfo.Mode().IsRegular():
				fmt.Fprintf(diagnostics, "pk: warning: workspace instructions %s is not a regular file; not loaded.\n", agentsPath)
			case readErr != nil:
				fmt.Fprintf(diagnostics, "pk: warning: cannot read workspace instructions %s: %v\n", agentsPath, readErr)
			case closeErr != nil:
				fmt.Fprintf(diagnostics, "pk: warning: cannot close workspace instructions %s: %v\n", agentsPath, closeErr)
			case len(contents) > maxWorkspaceInstructions:
				fmt.Fprintf(diagnostics, "pk: warning: %s exceeds the %d byte workspace-instructions limit; not loaded.\n", agentsPath, maxWorkspaceInstructions)
			default:
				sections = append(sections, "Workspace instructions (AGENTS.md):\n"+strings.TrimSpace(string(contents)))
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(diagnostics, "pk: warning: cannot inspect workspace instructions %s: %v\n", agentsPath, err)
	}
	if strings.TrimSpace(explicit) != "" {
		sections = append(sections, "Additional user instructions:\n"+strings.TrimSpace(explicit))
	}
	return strings.Join(sections, "\n\n")
}

func emitJSONLine(mu *sync.Mutex, out io.Writer, value any) error {
	mu.Lock()
	defer mu.Unlock()
	return writeJSONLine(out, value)
}

func writeJSONLine(out io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}

func submitStop(ctx context.Context, in *inbox.Inbox, mode inbox.ControlMode, reason string) error {
	payload, err := json.Marshal(inbox.ControlMessage{Mode: mode, Reason: reason})
	if err != nil {
		return err
	}
	inputID, err := newID()
	if err != nil {
		return err
	}
	return in.Submit(ctx, inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputControl, Payload: payload})
}

func newToolRegistry(workspace, operationDir string, dirs []string) (tool.Registry, []tool.Skill, []error) {
	var skills []tool.Skill
	var warnings []error
	for _, directory := range dirs {
		discovered, errs := tool.DiscoverSkills(directory)
		skills = append(skills, discovered...)
		warnings = append(warnings, errs...)
	}
	skills = deduplicateSkillFiles(skills)
	registry := newToolRegistryBaseWithSkills(workspace, operationDir, len(skills) > 0)
	for _, skill := range skills {
		if _, err := registry.RegisterSkill(skill); err != nil {
			warnings = append(warnings, err)
		}
	}
	return registry, registry.Skills(), warnings
}

func defaultRegistryFactory(options ToolRegistryOptions) (tool.Registry, []tool.Skill, []error) {
	if !options.SkillsSet {
		return newToolRegistry(options.Workspace, options.OperationDir, options.SkillsDirs)
	}
	registry := newToolRegistryBaseWithSkills(options.Workspace, options.OperationDir, len(options.Skills) > 0 || options.RequireSkillUse)
	var warnings []error
	for _, skill := range options.Skills {
		if _, err := registry.RegisterSkill(skill); err != nil {
			warnings = append(warnings, err)
		}
	}
	return registry, registry.Skills(), warnings
}

func newToolRegistryBase(workspace, operationDir string) tool.Registry {
	return newToolRegistryBaseWithSkills(workspace, operationDir, true)
}

func newToolRegistryBaseWithSkills(workspace, operationDir string, includeSkillUse bool) tool.Registry {
	enabled := []string{tool.BashName, tool.ViewImageName}
	if includeSkillUse {
		enabled = append(enabled, tool.SkillUseName)
	}
	return tool.NewRegistry(tool.StaticTranslators{
		Bash:      bash.New(bash.Config{Shell: "/bin/sh", Directory: workspace, BaseDirectory: operationDir}),
		ViewImage: imagehint.Wrap(viewimage.New(viewimage.Config{Directory: workspace})),
	}, enabled...)
}

func newContextSnapshot(options Options, registry tool.Registry, _ []tool.Skill) ContextSnapshot {
	tools := make([]llm.Tool, 0)
	for _, definition := range registry.StaticDefinitions() {
		tools = append(tools, definition.Tool)
	}
	explicit := strings.TrimSpace(options.SystemPrompt)
	return ContextSnapshot{
		Version: contextSnapshotVersion, Workspace: options.Workspace,
		SystemPrompt:         workspaceSystemPrompt(options.Workspace, options.SystemPrompt, options.Diagnostics),
		ExplicitSystemPrompt: explicit, Tools: tools, MCPFingerprint: options.MCPFingerprint, ProviderFingerprint: options.ProviderFingerprint, ProviderID: options.ProviderID, ImageGenFingerprint: options.ImageGenFingerprint,
	}
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// NewSessionID allocates a collision-resistant ID for a session that has not
// yet been created. Passing it as PreallocatedNewID does not resume a session.
func NewSessionID() (string, error) { return newID() }

func validPreallocatedSessionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
