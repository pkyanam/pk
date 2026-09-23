package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	defaultCallTimeout           = 10 * time.Second
	lifecycleCallTimeout         = 25 * time.Millisecond
	lifecycleCloseDrain          = 250 * time.Millisecond
	MaxSlashCommandArgumentBytes = 16 << 10
	MaxSlashCommandResultBytes   = 64 << 10
)

type Report struct {
	Registered []string
	Loaded     []string
	Disabled   []error
}

type loadedExtension struct {
	manifest        Manifest
	worker          Worker
	factory         WorkerFactory
	workspace       string
	startMu         sync.Mutex
	starting        *extensionStart
	lifecycle       map[string]bool
	lifecycleEvents chan LifecycleEvent
	disabled        error
}

type extensionStart struct {
	done   chan struct{}
	worker Worker
	err    error
}

// SlashCommand is a discoverable, explicitly invokable command registration.
// Name is fully qualified as /ext:<extension-id>:<command-name> so extension
// commands cannot shadow built-in slash commands or one another.
type SlashCommand struct {
	Name        string `json:"name"`
	ExtensionID string `json:"extension_id"`
	CommandName string `json:"command_name"`
	Description string `json:"description"`
}

type commandBinding struct {
	extension *loadedExtension
	name      string
}

type Host struct {
	ctx               context.Context
	cancel            context.CancelFunc
	workspace         string
	callTimeout       time.Duration
	mu                sync.RWMutex
	byID              map[string]*loadedExtension
	tools             map[string]*loadedExtension
	commands          map[string]*loadedExtension
	ambiguousCommands map[string]bool
	slashCommands     map[string]commandBinding
	closed            bool
	progressEvents    chan ProgressEvent
	progressHandler   func(ProgressEvent)
	lifecycleStop     chan struct{}
	lifecycleWG       sync.WaitGroup
	lifecycleDone     chan struct{}
	startWG           sync.WaitGroup
}

// NewHost loads only the manifests passed by its caller. Invalid or failing
// extensions are reported and isolated; valid extensions remain available.
func NewHost(parent context.Context, workspace string, manifests []Manifest, factory WorkerFactory) (*Host, Report, error) {
	if parent == nil {
		parent = context.Background()
	}
	if factory == nil {
		factory = ProcessFactory
	}
	workspace, err := ensureWorkspace(workspace)
	if err != nil {
		return nil, Report{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	host := &Host{ctx: ctx, cancel: cancel, workspace: workspace, callTimeout: defaultCallTimeout,
		byID: make(map[string]*loadedExtension), tools: make(map[string]*loadedExtension), commands: make(map[string]*loadedExtension),
		ambiguousCommands: make(map[string]bool), slashCommands: make(map[string]commandBinding), progressEvents: make(chan ProgressEvent, MaxProgressEventsPerCall), lifecycleStop: make(chan struct{}), lifecycleDone: make(chan struct{})}
	go host.dispatchProgress()
	report := Report{}
	for _, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			report.Disabled = append(report.Disabled, err)
			continue
		}
		if _, exists := host.byID[manifest.ID]; exists {
			report.Disabled = append(report.Disabled, fmt.Errorf("duplicate extension ID %q", manifest.ID))
			continue
		}
		if conflict := host.nameConflict(manifest); conflict != nil {
			report.Disabled = append(report.Disabled, conflict)
			continue
		}
		loaded := &loadedExtension{manifest: manifest, factory: factory, workspace: workspace}
		if len(manifest.Hooks) > 0 {
			loaded.lifecycle = make(map[string]bool, len(manifest.Hooks))
			for _, hook := range manifest.Hooks {
				loaded.lifecycle[hook.Event] = true
			}
			loaded.lifecycleEvents = make(chan LifecycleEvent, MaxLifecycleQueue)
			// Lifecycle observers must receive run-start even when the model never
			// invokes one of their tools, so their worker remains eager.
			if err := host.startExtension(ctx, loaded); err != nil {
				report.Disabled = append(report.Disabled, fmt.Errorf("initialize extension %q: %w", manifest.ID, err))
				continue
			}
			report.Loaded = append(report.Loaded, manifest.ID)
		}
		host.byID[manifest.ID] = loaded
		if loaded.lifecycleEvents != nil {
			host.lifecycleWG.Add(1)
			go host.dispatchLifecycle(loaded)
		}
		for _, spec := range manifest.Tools {
			host.tools[spec.Name] = loaded
		}
		for _, spec := range manifest.Commands {
			if _, exists := host.commands[spec.Name]; exists {
				host.commands[spec.Name] = nil
				host.ambiguousCommands[spec.Name] = true
			} else {
				host.commands[spec.Name] = loaded
			}
			slashName := SlashCommandName(manifest.ID, spec.Name)
			host.slashCommands[slashName] = commandBinding{extension: loaded, name: spec.Name}
		}
		report.Registered = append(report.Registered, manifest.ID)
	}
	go func() { host.lifecycleWG.Wait(); close(host.lifecycleDone) }()
	return host, report, nil
}

func (h *Host) startExtension(ctx context.Context, ext *loadedExtension) error {
	worker, err := h.initializeWorker(ctx, ext)
	if err != nil {
		return err
	}
	ext.startMu.Lock()
	ext.worker = worker
	ext.startMu.Unlock()
	return nil
}

// ensureWorker starts a registered extension on its first command/tool use.
// Concurrent first calls share one initialization attempt. A canceled caller
// stops waiting promptly; initialization remains session-scoped and the next
// call can join the same attempt.
func (h *Host) ensureWorker(ctx context.Context, ext *loadedExtension) (Worker, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		h.mu.RLock()
		if h.closed {
			h.mu.RUnlock()
			return nil, errors.New("extension host is closed")
		}
		ext.startMu.Lock()
		if ext.disabled != nil {
			err := ext.disabled
			ext.startMu.Unlock()
			h.mu.RUnlock()
			return nil, fmt.Errorf("extension %q is disabled: %w", ext.manifest.ID, err)
		}
		if ext.worker != nil {
			worker := ext.worker
			ext.startMu.Unlock()
			h.mu.RUnlock()
			return worker, nil
		}
		attempt := ext.starting
		if attempt != nil {
			ext.startMu.Unlock()
			h.mu.RUnlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-attempt.done:
			}
			if attempt.err != nil {
				return nil, attempt.err
			}
			return attempt.worker, nil
		}
		attempt = &extensionStart{done: make(chan struct{})}
		ext.starting = attempt
		ext.startMu.Unlock()
		h.mu.RUnlock()

		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			err := errors.New("extension host is closed")
			ext.finishStart(attempt, nil, err, false)
			return nil, err
		}
		h.startWG.Add(1)
		h.mu.Unlock()
		go func() {
			defer h.startWG.Done()
			worker, err := h.initializeWorker(h.ctx, ext)
			if err != nil {
				// Host shutdown cancellation is retryable; a real load or protocol
				// failure disables the extension for this frozen session.
				ext.finishStart(attempt, nil, err, !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded))
				return
			}
			h.mu.Lock()
			if h.closed {
				h.mu.Unlock()
				_ = worker.Close()
				ext.finishStart(attempt, nil, errors.New("extension host closed during initialization"), false)
				return
			}
			// Publish under the host lock. Close marks the host closed before
			// waiting for startWG, so it cannot miss a newly initialized worker.
			ext.finishStart(attempt, worker, nil, false)
			h.mu.Unlock()
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-attempt.done:
		}
		if attempt.err != nil {
			return nil, attempt.err
		}
		return attempt.worker, nil
	}
}

func (ext *loadedExtension) finishStart(attempt *extensionStart, worker Worker, err error, disable bool) {
	ext.startMu.Lock()
	attempt.worker = worker
	attempt.err = err
	if worker != nil {
		ext.worker = worker
	}
	if disable && err != nil && ext.disabled == nil {
		ext.disabled = err
	}
	if ext.starting == attempt {
		ext.starting = nil
	}
	close(attempt.done)
	ext.startMu.Unlock()
}

func (h *Host) initializeWorker(ctx context.Context, ext *loadedExtension) (Worker, error) {
	worker, err := ext.factory(ctx, ext.manifest, ext.workspace)
	if err != nil {
		return nil, fmt.Errorf("start extension %q: %w", ext.manifest.ID, err)
	}
	initParams := InitializeParams{APIVersion: ProtocolVersion, ID: ext.manifest.ID, Version: ext.manifest.Version, Workspace: ext.workspace, Capabilities: append([]string(nil), ext.manifest.Capabilities...), HostFeatures: []string{HostFeatureToolProgress, HostFeatureLifecycle}}
	var initResult InitializeResult
	initCtx, initCancel := context.WithTimeout(ctx, defaultCallTimeout)
	err = worker.Call(initCtx, "initialize", initParams, &initResult)
	initCancel()
	if err == nil {
		err = validateInitialize(ext.manifest, initResult)
	}
	if err != nil {
		_ = worker.Close()
		return nil, fmt.Errorf("initialize extension %q: %w", ext.manifest.ID, err)
	}
	return worker, nil
}

func validateInitialize(manifest Manifest, result InitializeResult) error {
	if result.APIVersion != ProtocolVersion || result.ID != manifest.ID {
		return fmt.Errorf("handshake identity/version mismatch: got id=%q api=%q", result.ID, result.APIVersion)
	}
	if !sameNames(namesOfTools(manifest.Tools), result.Tools) {
		return errors.New("worker tool registrations do not match manifest")
	}
	if !sameNames(namesOfCommands(manifest.Commands), result.Commands) {
		return errors.New("worker command registrations do not match manifest")
	}
	features := make(map[string]bool, len(result.Features))
	for _, feature := range result.Features {
		if feature != HostFeatureLifecycle && feature != HostFeatureToolProgress {
			return fmt.Errorf("worker declared unsupported feature %q", feature)
		}
		if features[feature] {
			return fmt.Errorf("worker repeated feature %q", feature)
		}
		features[feature] = true
	}
	if len(manifest.Hooks) > 0 && !features[HostFeatureLifecycle] {
		return errors.New("manifest declares lifecycle observers but worker did not opt in with lifecycle_notifications")
	}
	return nil
}

func namesOfTools(specs []ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func namesOfCommands(specs []CommandSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func sameNames(want, got []string) bool {
	if len(want) != len(got) {
		return false
	}
	left, right := append([]string(nil), want...), append([]string(nil), got...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (h *Host) nameConflict(manifest Manifest) error {
	for _, spec := range manifest.Tools {
		if owner := h.tools[spec.Name]; owner != nil {
			return fmt.Errorf("extension %q tool %q conflicts with extension %q", manifest.ID, spec.Name, owner.manifest.ID)
		}
	}
	return nil
}

func (h *Host) Tools() []ToolSpec {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []ToolSpec
	for _, manifest := range h.orderedManifests() {
		for _, spec := range manifest.Tools {
			spec.Parameters = append(json.RawMessage(nil), spec.Parameters...)
			out = append(out, spec)
		}
	}
	return out
}

func (h *Host) Commands() []CommandSpec {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []CommandSpec
	for _, manifest := range h.orderedManifests() {
		out = append(out, manifest.Commands...)
	}
	return out
}

// SlashCommands returns loaded command declarations in deterministic order.
// It performs no command execution. Extensions are loaded only from manifests
// explicitly passed to NewHost.
func (h *Host) SlashCommands() []SlashCommand {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []SlashCommand
	for _, manifest := range h.orderedManifests() {
		for _, spec := range manifest.Commands {
			out = append(out, SlashCommand{
				Name: SlashCommandName(manifest.ID, spec.Name), ExtensionID: manifest.ID,
				CommandName: spec.Name, Description: spec.Description,
			})
		}
	}
	return out
}

// SlashCommandName returns the stable user-facing namespace for a command.
func SlashCommandName(extensionID, commandName string) string {
	return "/ext:" + extensionID + ":" + commandName
}

// SchemaFingerprint identifies the enabled extension tool declarations so a
// session host can persist and compare them before restoring model tool state.
func (h *Host) SchemaFingerprint() string {
	type fingerprintTool struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	}
	type fingerprintExtension struct {
		ID      string            `json:"id"`
		Version string            `json:"version"`
		Tools   []fingerprintTool `json:"tools"`
	}
	var payload []fingerprintExtension
	for _, manifest := range h.orderedManifests() {
		entry := fingerprintExtension{ID: manifest.ID, Version: manifest.Version}
		for _, spec := range manifest.Tools {
			var schema any
			_ = json.Unmarshal(spec.Parameters, &schema)
			entry.Tools = append(entry.Tools, fingerprintTool{Name: spec.Name, Description: spec.Description, Parameters: schema})
		}
		sort.Slice(entry.Tools, func(i, j int) bool { return entry.Tools[i].Name < entry.Tools[j].Name })
		payload = append(payload, entry)
	}
	encoded, _ := json.Marshal(struct {
		API        string                 `json:"api"`
		Extensions []fingerprintExtension `json:"extensions"`
	}{ProtocolVersion, payload})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (h *Host) rejectToolName(name string, cause error) {
	h.mu.RLock()
	ext := h.tools[name]
	h.mu.RUnlock()
	if ext == nil {
		return
	}
	h.disable(ext, cause)
}

func (h *Host) ownerOfTool(name string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if ext := h.tools[name]; ext != nil {
		return ext.manifest.ID
	}
	return ""
}

func (h *Host) disable(ext *loadedExtension, cause error) {
	ext.startMu.Lock()
	if ext.disabled == nil {
		ext.disabled = cause
	}
	worker := ext.worker
	ext.startMu.Unlock()
	if worker != nil {
		_ = worker.Close()
	}
}

func (h *Host) orderedManifests() []Manifest {
	items := make([]Manifest, 0, len(h.byID))
	for _, ext := range h.byID {
		items = append(items, ext.manifest)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (h *Host) ExecuteTool(ctx context.Context, name, callID string, arguments json.RawMessage) (ToolResult, error) {
	ext := h.lookup(h.tools, name)
	if ext == nil {
		return ToolResult{}, fmt.Errorf("extension tool %q is unavailable", name)
	}
	worker, err := h.ensureWorker(ctx, ext)
	if err != nil {
		return ToolResult{}, fmt.Errorf("extension %q is unavailable: %w", ext.manifest.ID, err)
	}
	callCtx, cancel := h.callContext(ctx)
	defer cancel()
	var progressCount atomic.Int32
	callCtx = context.WithValue(callCtx, progressReporterKey{}, func(text string) bool {
		if callCtx.Err() != nil || progressCount.Add(1) > MaxProgressEventsPerCall {
			return false
		}
		return h.enqueueProgress(ProgressEvent{ExtensionID: ext.manifest.ID, ToolName: name, CallID: callID, Text: sanitizeProgress(text)})
	})
	var result ToolResult
	err = worker.Call(callCtx, "tool.execute", ToolExecuteParams{Name: name, CallID: callID, Arguments: arguments, Workspace: h.workspace}, &result)
	if err != nil {
		h.failWorker(ext, err)
		return ToolResult{}, fmt.Errorf("extension %q tool %q: %w", ext.manifest.ID, name, err)
	}
	if result.Error != "" {
		return result, nil
	}
	if len(result.Content) == 0 {
		return ToolResult{}, errors.New("extension tool returned no content")
	}
	for _, item := range result.Content {
		if item.Type != "text" {
			return ToolResult{}, fmt.Errorf("unsupported extension result type %q", item.Type)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxMessageSize {
		return ToolResult{}, errors.New("extension tool result is invalid or exceeds protocol limit")
	}
	return result, nil
}

// SetProgressHandler replaces the asynchronous progress consumer. It may be
// called concurrently with tool execution; callbacks already in flight may
// use the previous handler. The handler must return promptly. It is never
// called from the extension process reader, and queue overflow drops updates.
// Close does not wait for an in-flight handler callback.
func (h *Host) SetProgressHandler(handler func(ProgressEvent)) {
	h.mu.Lock()
	h.progressHandler = handler
	h.mu.Unlock()
}

func (h *Host) enqueueProgress(event ProgressEvent) bool {
	event.Text = sanitizeProgress(event.Text)
	if event.Text == "" {
		return false
	}
	h.mu.RLock()
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		return false
	}
	select {
	case h.progressEvents <- event:
		return true
	default:
		return false
	}
}

// NotifyLifecycle queues an observer event without waiting for an extension
// process. It returns false when no opted-in observer declared the event or
// every relevant bounded queue is full. The event workspace is always replaced
// with the host's canonical workspace. Events contain metadata only.
func (h *Host) NotifyLifecycle(event LifecycleEvent) bool {
	if !validLifecycleEvent(event) {
		return false
	}
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return false
	}
	event.Workspace = h.workspace
	accepted := false
	for _, ext := range h.byID {
		if ext.lifecycleEvents == nil || !ext.lifecycle[event.Type] {
			continue
		}
		select {
		case ext.lifecycleEvents <- event:
			accepted = true
		default:
			// Observer backpressure never reaches the run that emitted it.
		}
	}
	h.mu.RUnlock()
	return accepted
}

func validLifecycleEvent(event LifecycleEvent) bool {
	switch event.Type {
	case LifecycleRunStart, LifecycleResponseComplete, LifecycleRunEnd:
	default:
		return false
	}
	for _, value := range []string{event.RunID, event.SessionID, event.Model, event.Status} {
		if !utf8.ValidString(value) || len(value) > 512 {
			return false
		}
	}
	return true
}

func (h *Host) dispatchLifecycle(ext *loadedExtension) {
	defer h.lifecycleWG.Done()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.lifecycleStop:
			for {
				select {
				case event := <-ext.lifecycleEvents:
					h.deliverLifecycle(ext, event)
				default:
					return
				}
			}
		case event := <-ext.lifecycleEvents:
			h.deliverLifecycle(ext, event)
		}
	}
}

func (h *Host) deliverLifecycle(ext *loadedExtension, event LifecycleEvent) {
	worker := ext.worker
	if worker == nil {
		return
	}
	if notifier, ok := worker.(LifecycleNotifier); ok {
		notifier.NotifyLifecycle(event)
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, lifecycleCallTimeout)
	defer cancel()
	var ignored struct{}
	// Notification failures are isolated from tool/command execution and do
	// not disable the extension or alter model-visible content.
	_ = worker.Call(ctx, "lifecycle.notify", event, &ignored)
}

func (h *Host) dispatchProgress() {
	for {
		select {
		case <-h.ctx.Done():
			return
		case event := <-h.progressEvents:
			select {
			case <-h.ctx.Done():
				return
			default:
			}
			h.mu.RLock()
			handler := h.progressHandler
			h.mu.RUnlock()
			if handler != nil {
				func() { defer func() { _ = recover() }(); handler(event) }()
			}
		}
	}
}

func (h *Host) ExecuteCommand(ctx context.Context, name, arguments string) (string, error) {
	h.mu.RLock()
	ambiguous := h.ambiguousCommands[name]
	h.mu.RUnlock()
	if ambiguous {
		return "", fmt.Errorf("extension command %q is ambiguous; use its namespaced slash command", name)
	}
	ext := h.lookup(h.commands, name)
	if ext == nil {
		return "", fmt.Errorf("extension command %q is unavailable", name)
	}
	if _, err := h.ensureWorker(ctx, ext); err != nil {
		return "", fmt.Errorf("extension %q is unavailable: %w", ext.manifest.ID, err)
	}
	return h.executeCommand(ctx, ext, name, arguments)
}

// ExecuteSlashCommand invokes one discovered command by its fully qualified
// name, keeping the extension ID out of the worker's stable v1 command name.
func (h *Host) ExecuteSlashCommand(ctx context.Context, name, arguments string) (string, error) {
	h.mu.RLock()
	binding, exists := h.slashCommands[name]
	closed := h.closed
	h.mu.RUnlock()
	if closed || !exists {
		return "", fmt.Errorf("extension slash command %q is unavailable", name)
	}
	if h.lookup(map[string]*loadedExtension{binding.name: binding.extension}, binding.name) == nil {
		return "", fmt.Errorf("extension slash command %q is unavailable", name)
	}
	if _, err := h.ensureWorker(ctx, binding.extension); err != nil {
		return "", fmt.Errorf("extension %q is unavailable: %w", binding.extension.manifest.ID, err)
	}
	return h.executeCommand(ctx, binding.extension, binding.name, arguments)
}

func (h *Host) executeCommand(ctx context.Context, ext *loadedExtension, name, arguments string) (string, error) {
	if len(arguments) > MaxSlashCommandArgumentBytes {
		return "", fmt.Errorf("extension command arguments exceed %d bytes", MaxSlashCommandArgumentBytes)
	}
	if !utf8.ValidString(arguments) {
		return "", errors.New("extension command arguments must be valid UTF-8")
	}
	callCtx, cancel := h.callContext(ctx)
	defer cancel()
	var result string
	err := ext.worker.Call(callCtx, "command.execute", CommandExecuteParams{Name: name, Arguments: arguments, Workspace: h.workspace}, &result)
	if err != nil {
		h.failWorker(ext, err)
		return "", fmt.Errorf("extension %q command %q: %w", ext.manifest.ID, name, err)
	}
	if len(result) > MaxSlashCommandResultBytes {
		return "", fmt.Errorf("extension command result exceeds %d bytes", MaxSlashCommandResultBytes)
	}
	if !utf8.ValidString(result) {
		return "", errors.New("extension command result is not valid UTF-8")
	}
	return result, nil
}

func (h *Host) lookup(table map[string]*loadedExtension, name string) *loadedExtension {
	h.mu.RLock()
	ext := table[name]
	closed := h.closed
	h.mu.RUnlock()
	if closed || ext == nil {
		return nil
	}
	ext.startMu.Lock()
	disabled := ext.disabled
	ext.startMu.Unlock()
	if disabled != nil {
		return nil
	}
	return ext
}

func (h *Host) startedWorkers() []Worker {
	h.mu.RLock()
	defer h.mu.RUnlock()
	workers := make([]Worker, 0, len(h.byID))
	for _, ext := range h.byID {
		ext.startMu.Lock()
		worker := ext.worker
		ext.startMu.Unlock()
		if worker != nil {
			workers = append(workers, worker)
		}
	}
	return workers
}

func (h *Host) failWorker(ext *loadedExtension, err error) {
	var remote *RPCError
	if errors.As(err, &remote) {
		return
	}
	h.disable(ext, err)
}

func (h *Host) callContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	h.mu.RLock()
	timeout := h.callTimeout
	h.mu.RUnlock()
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := context.AfterFunc(h.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

func (h *Host) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	close(h.lifecycleStop)
	h.mu.Unlock()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), lifecycleCloseDrain)
	select {
	case <-h.lifecycleDone:
	case <-drainCtx.Done():
	}
	workers := h.startedWorkers()
	if drainCtx.Err() == nil {
		for _, worker := range workers {
			if drainer, ok := worker.(LifecycleDrainer); ok {
				_ = drainer.WaitLifecycle(drainCtx)
			}
		}
	}
	drainCancel()
	h.cancel()
	h.startWG.Wait()
	workers = h.startedWorkers()
	var closeErr error
	for _, worker := range workers {
		if err := worker.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (h *Host) SetCallTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("extension call timeout must be positive")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("extension host is closed")
	}
	h.callTimeout = timeout
	return nil
}

func LoadManifests(paths []string) ([]Manifest, []error) {
	manifests := make([]Manifest, 0, len(paths))
	var issues []error
	for _, path := range paths {
		manifest, err := LoadManifest(path)
		if err != nil {
			issues = append(issues, fmt.Errorf("%s: %w", path, err))
			continue
		}
		manifests = append(manifests, manifest)
	}
	return manifests, issues
}

func (h *Host) Wait() error { <-h.ctx.Done(); return h.ctx.Err() }

func ensureWorkspace(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace %q is not a directory", absolute)
	}
	return absolute, nil
}
