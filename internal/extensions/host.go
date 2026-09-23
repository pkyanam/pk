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
	"time"
	"unicode/utf8"
)

const (
	defaultCallTimeout           = 10 * time.Second
	MaxSlashCommandArgumentBytes = 16 << 10
	MaxSlashCommandResultBytes   = 64 << 10
)

type Report struct {
	Loaded   []string
	Disabled []error
}

type loadedExtension struct {
	manifest Manifest
	worker   Worker
	mu       sync.Mutex
	disabled error
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
		ambiguousCommands: make(map[string]bool), slashCommands: make(map[string]commandBinding)}
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
		worker, err := factory(ctx, manifest, workspace)
		if err != nil {
			report.Disabled = append(report.Disabled, fmt.Errorf("load extension %q: %w", manifest.ID, err))
			continue
		}
		loaded := &loadedExtension{manifest: manifest, worker: worker}
		initParams := InitializeParams{APIVersion: ProtocolVersion, ID: manifest.ID, Version: manifest.Version, Workspace: workspace, Capabilities: append([]string(nil), manifest.Capabilities...)}
		var initResult InitializeResult
		initCtx, initCancel := context.WithTimeout(ctx, defaultCallTimeout)
		err = worker.Call(initCtx, "initialize", initParams, &initResult)
		initCancel()
		if err == nil {
			err = validateInitialize(manifest, initResult)
		}
		if err != nil {
			_ = worker.Close()
			report.Disabled = append(report.Disabled, fmt.Errorf("initialize extension %q: %w", manifest.ID, err))
			continue
		}
		host.byID[manifest.ID] = loaded
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
		report.Loaded = append(report.Loaded, manifest.ID)
	}
	return host, report, nil
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
	ext.mu.Lock()
	if ext.disabled == nil {
		ext.disabled = cause
	}
	ext.mu.Unlock()
	_ = ext.worker.Close()
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
	callCtx, cancel := h.callContext(ctx)
	defer cancel()
	var result ToolResult
	err := ext.worker.Call(callCtx, "tool.execute", ToolExecuteParams{Name: name, CallID: callID, Arguments: arguments, Workspace: h.workspace}, &result)
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
	ext.mu.Lock()
	disabled := ext.disabled
	ext.mu.Unlock()
	if disabled != nil {
		return nil
	}
	return ext
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
	h.cancel()
	workers := make([]Worker, 0, len(h.byID))
	for _, ext := range h.byID {
		workers = append(workers, ext.worker)
	}
	h.mu.Unlock()
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
