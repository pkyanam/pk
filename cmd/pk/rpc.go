package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/clipboard"
	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/extensions"
	"github.com/pkyanam/pk/internal/imagegen"
	"github.com/pkyanam/pk/internal/interaction"
	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/modelstream"
	"github.com/pkyanam/pk/internal/presentation"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/skillinstall"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/pkyanam/pk/internal/tasks"
	"github.com/pkyanam/pk/internal/websearch"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const rpcVersion = 1
const rpcClipboardTimeout = 5 * time.Second

type rpcMessage struct {
	Version int             `json:"version"`
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
type rpcEvent struct {
	Version int    `json:"version"`
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

func rpcMain(ctx context.Context, input io.Reader, output, diagnostics io.Writer) int {
	server := &rpcServer{ctx: ctx, input: input, output: output, diagnostics: diagnostics, cfgPath: filepath.Join(pkHome(), "config.json"), sessionDir: filepath.Join(pkHome(), "sessions"), requestTypes: make(map[string]string)}
	if err := server.serve(); err != nil {
		fmt.Fprintf(diagnostics, "pk rpc: %v\n", err)
		return 1
	}
	return 0
}

type rpcServer struct {
	ctx                        context.Context
	input                      io.Reader
	output, diagnostics        io.Writer
	cfgPath, sessionDir        string
	mu                         sync.Mutex
	opts                       runner.Options
	contextBudgetConfig        config.ContextBudgetConfig
	historyCompactionConfig    config.HistoryCompactionConfig
	adapter                    *codexAdapter
	useCodex                   bool
	codexPath                  string
	started                    bool
	steeringEnabled            bool
	providerReasoningRequested bool
	providerReasoningEnabled   bool
	session                    string
	providerID                 string
	providerModelsRequestID    string
	providerModelsCancel       context.CancelFunc
	pendingProviderModels      *providerModelsRequest
	activeCancel               context.CancelFunc
	releaseCancel              context.CancelFunc
	releaseDone                chan struct{}
	releaseActive              bool
	pluginCommandCancel        context.CancelFunc
	pluginCommandDone          chan struct{}
	pluginCommandActive        bool
	skillOperationCancel       context.CancelFunc
	skillOperationDone         chan struct{}
	skillOperationActive       bool
	skillOperationKind         string
	skillOperationRequestID    string
	reloadPrepared             bool
	activeInputs               chan runner.Input
	activeSteerRequests        chan steeringRequest
	pendingSteers              map[string]func(error)
	active                     bool
	quit                       bool
	attachedTask               string
	taskFollowRequest          string
	taskFollowCancel           context.CancelFunc
	pluginPaths                []string
	pluginIssues               []string
	requestTypes               map[string]string
	skillManagerFactory        func() skillinstall.Manager
	broker                     *interaction.Broker
	loadAttachments            func(context.Context, string, string, []string) (string, []attachments.Attachment, error)
	prepareAdapter             func(context.Context, bool, string) (*codexAdapter, error)
	clipboardProvider          clipboard.Provider
	clipboardWriter            clipboard.TextWriter
	clipboardActive            bool
	clipboardLateWrite         bool
	clipboardTimeout           time.Duration
	runReleaseCommand          func(context.Context, []string, io.Writer, io.Writer) int
}

func (s *rpcServer) emit(id, typ string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if typ == "error" && id != "" && s.requestTypes[id] != "" {
		if object, ok := payload.(map[string]any); ok {
			copy := make(map[string]any, len(object)+1)
			for key, value := range object {
				copy[key] = value
			}
			copy["request_type"] = s.requestTypes[id]
			payload = copy
		}
	}
	return json.NewEncoder(s.output).Encode(rpcEvent{Version: rpcVersion, ID: id, Type: typ, Payload: payload})
}

type clipboardOperationResult struct {
	eventType string
	payload   any
	err       error
	finished  time.Time
}

// startClipboardOperation keeps native clipboard helpers off the RPC reader
// loop. The single slot remains occupied after timeout until the helper really
// returns: AppKit calls cannot be interrupted once entered, and a late write
// must not race a newer pk write.
func (s *rpcServer) startClipboardOperation(requestID string, mayWriteLate bool, run func(context.Context) (string, any, error)) {
	s.mu.Lock()
	if s.clipboardActive {
		lateWrite := s.clipboardLateWrite
		s.mu.Unlock()
		payload := map[string]any{"message": "clipboard operation is still in progress", "recoverable": true, "clipboard_busy": true}
		if lateWrite {
			payload["native_may_complete_late"] = true
		}
		_ = s.emit(requestID, "error", payload)
		return
	}
	s.clipboardActive = true
	s.clipboardLateWrite = mayWriteLate
	s.mu.Unlock()

	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	timeout := s.clipboardTimeout
	if timeout <= 0 {
		timeout = rpcClipboardTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	deadline := time.Now().Add(timeout)
	done := make(chan clipboardOperationResult, 1)
	go func() {
		eventType, payload, err := run(ctx)
		done <- clipboardOperationResult{eventType: eventType, payload: payload, err: err, finished: time.Now()}
	}()
	go func() {
		defer cancel()
		finish := func() {
			s.mu.Lock()
			s.clipboardActive = false
			s.clipboardLateWrite = false
			s.mu.Unlock()
		}
		publish := func(result clipboardOperationResult) {
			if result.err != nil {
				_ = s.emit(requestID, "error", map[string]any{"message": result.err.Error(), "recoverable": true})
				return
			}
			_ = s.emit(requestID, result.eventType, result.payload)
		}
		select {
		case result := <-done:
			finish()
			if parent.Err() != nil {
				return
			}
			if !result.finished.Before(deadline) {
				payload := map[string]any{"message": "clipboard operation timed out", "recoverable": true, "clipboard_operation_timeout": true}
				if mayWriteLate {
					payload["native_may_complete_late"] = true
				}
				_ = s.emit(requestID, "error", payload)
				return
			}
			publish(result)
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && parent.Err() == nil {
				payload := map[string]any{"message": "clipboard operation timed out", "recoverable": true, "clipboard_operation_timeout": true}
				if mayWriteLate {
					payload["native_may_complete_late"] = true
				}
				_ = s.emit(requestID, "error", payload)
			}
			<-done // retain the slot until a non-cancellable native call returns
			finish()
		}
	}()
}

func (s *rpcServer) rejectSteer(requestID, message string) {
	s.mu.Lock()
	sessionID := s.session
	s.mu.Unlock()
	_ = s.emit(requestID, "input_rejected", map[string]any{"input_id": requestID, "session_id": sessionID, "message": message})
}

// relayRPCPluginProgress keeps extension progress out of runner/model output
// and keeps the extension-host callback off the RPC output path. The extension
// host already bounds and sanitizes events; this extra bounded queue drops
// progress if the client cannot keep up.
func relayRPCPluginProgress(ctx context.Context, server *rpcServer, host *extensions.Host, requestID string) func() {
	if host == nil || server == nil {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	progressCtx, cancel := context.WithCancel(ctx)
	queue := make(chan extensions.ProgressEvent, 16)
	host.SetProgressHandler(func(event extensions.ProgressEvent) {
		select {
		case <-progressCtx.Done():
			return
		default:
		}
		select {
		case queue <- event:
		default:
			// Progress is best effort. A slow or disconnected UI must not
			// backpressure the extension worker or delay tool completion.
		}
	})
	go func() {
		for {
			select {
			case <-progressCtx.Done():
				return
			case event := <-queue:
				if progressCtx.Err() != nil {
					return
				}
				_ = server.emit(requestID, "tool_progress", map[string]any{
					"call_id": event.CallID, "name": event.ToolName,
					"extension_id": event.ExtensionID, "text": event.Text,
				})
			}
		}
	}()
	return func() {
		host.SetProgressHandler(nil)
		cancel()
	}
}

func (s *rpcServer) startReleaseOperation(requestID, operation, sourcePath string) {
	if operation == "update" && strings.TrimSpace(sourcePath) != "" {
		resolved, err := filepath.Abs(strings.TrimSpace(sourcePath))
		if err != nil {
			_ = s.emit(requestID, "error", map[string]any{"message": "resolve update source path: " + err.Error(), "recoverable": true})
			return
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			_ = s.emit(requestID, "error", map[string]any{"message": "update source must be an existing directory", "recoverable": true})
			return
		}
		sourcePath = resolved
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "send start before updating", "recoverable": true})
		return
	}
	if s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "update and rollback require an idle session with no attached task", "recoverable": true})
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.releaseActive = true
	s.releaseCancel = cancel
	s.releaseDone = done
	s.mu.Unlock()
	_ = s.emit(requestID, operation+"_started", map[string]any{"source_path": sourcePath})
	progress := "Preparing the update; validation and build stages will appear as they run."
	if operation == "rollback" {
		progress = "Restoring the previous managed release."
	}
	_ = s.emit(requestID, operation+"_progress", map[string]any{"text": progress})
	go func() {
		writer := &rpcReleaseProgressWriter{server: s, requestID: requestID, eventType: operation + "_progress"}
		args := []string{operation}
		if operation == "update" && sourcePath != "" {
			args = append(args, "--source", sourcePath)
		}
		runCommand := s.runReleaseCommand
		if runCommand == nil {
			runCommand = runUpdateCommand
		}
		var code int
		if operation == "update" && s.runReleaseCommand == nil {
			code = runUpdateWithProgress(ctx, args[1:], writer, writer, func(stage string) {
				_ = s.emit(requestID, "update_progress", map[string]any{"stage": stage, "text": updateStageMessage(stage)})
			})
		} else {
			code = runCommand(ctx, args, writer, writer)
		}
		message := strings.TrimSpace(writer.String())
		if message == "" {
			if code == 0 {
				message = "Operation completed."
			} else if ctx.Err() != nil {
				message = "Operation canceled."
			} else {
				message = "Operation failed; see progress output for details."
			}
		}
		s.mu.Lock()
		s.releaseActive = false
		s.releaseCancel = nil
		s.mu.Unlock()
		_ = s.emit(requestID, operation+"_finished", map[string]any{"success": code == 0, "cancelled": ctx.Err() != nil, "exit_code": code, "message": message})
		close(done)
	}()
}

func updateStageMessage(stage string) string {
	messages := map[string]string{
		"release_check":    "Checking for the latest published pk release.",
		"release_download": "Downloading the compatible pk release and checksum list.",
		"release_verify":   "Verifying the release archive before staging it.",
		"source_fallback":  "No compatible published release is available; switching to a source build.",
		"source_validate":  "Validating the selected pk source.",
		"clone":            "Cloning the official pk source into an isolated checkout.",
		"resolve_revision": "Resolving the requested pk revision.",
		"copy":             "Copying source into an isolated build directory.",
		"test":             "Running Go tests.",
		"dependencies":     "Preparing UI dependencies from the lockfile.",
		"ui_validate":      "Checking and testing the UI.",
		"build":            "Building the release artifacts.",
		"stage":            "Publishing the staged release.",
		"activate":         "Activating the release and updating the stable launcher.",
	}
	if message := messages[stage]; message != "" {
		return message
	}
	return "Updating pk."
}

type rpcReleaseProgressWriter struct {
	mu        sync.Mutex
	server    *rpcServer
	requestID string
	eventType string
	text      strings.Builder
}

func (w *rpcReleaseProgressWriter) Write(data []byte) (int, error) {
	const maxOutput = 16 << 10
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.text.Len() < maxOutput {
		remaining := maxOutput - w.text.Len()
		chunk := data
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		w.text.Write(chunk)
	}
	if len(data) > 0 {
		const eventChunkLimit = 4096
		chunk := data
		truncated := false
		if len(chunk) > eventChunkLimit {
			chunk = chunk[:eventChunkLimit]
			truncated = true
		}
		_ = w.server.emit(w.requestID, w.eventType, map[string]any{"text": string(chunk), "truncated": truncated})
	}
	return len(data), nil
}

func (w *rpcReleaseProgressWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.text.String())
}

func (s *rpcServer) serve() error {
	scanner := bufio.NewScanner(s.input)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	type line struct {
		data []byte
		err  error
	}
	lines := make(chan line, 1)
	go func() {
		for scanner.Scan() {
			lines <- line{data: append([]byte(nil), scanner.Bytes()...)}
		}
		lines <- line{err: scanner.Err()}
	}()
	finished := make(chan turnDone, 1)
	for !s.quit {
		select {
		case result := <-finished:
			s.completeTurn(result)
		case input := <-lines:
			if input.err != nil {
				if input.err != io.EOF {
					return input.err
				}
				s.quit = true
				continue
			}
			if input.data == nil {
				s.quit = true
				continue
			}
			var msg rpcMessage
			if err := json.Unmarshal(input.data, &msg); err != nil {
				_ = s.emit("", "error", map[string]any{"message": "invalid JSON command: " + err.Error(), "recoverable": true})
				continue
			}
			if msg.Version != rpcVersion {
				_ = s.emit(msg.ID, "error", map[string]any{"message": fmt.Sprintf("unsupported protocol version %d", msg.Version), "recoverable": false})
				continue
			}
			s.handle(msg, finished)
		}
	}
	if s.activeCancel != nil {
		s.activeCancel()
		s.completeTurn(<-finished)
	}
	s.mu.Lock()
	releaseCancel, releaseDone := s.releaseCancel, s.releaseDone
	s.mu.Unlock()
	if releaseCancel != nil {
		releaseCancel()
	}
	if releaseDone != nil {
		<-releaseDone
	}
	s.mu.Lock()
	pluginCommandCancel, pluginCommandDone := s.pluginCommandCancel, s.pluginCommandDone
	s.mu.Unlock()
	if pluginCommandCancel != nil {
		pluginCommandCancel()
	}
	if pluginCommandDone != nil {
		<-pluginCommandDone
	}
	s.mu.Lock()
	skillOperationCancel, skillOperationDone := s.skillOperationCancel, s.skillOperationDone
	s.mu.Unlock()
	if skillOperationCancel != nil {
		skillOperationCancel()
	}
	if skillOperationDone != nil {
		<-skillOperationDone
	}
	if s.taskFollowCancel != nil {
		s.taskFollowCancel()
	}
	if s.adapter != nil {
		_ = s.adapter.Close()
	}
	return nil
}

func (s *rpcServer) completeTurn(result turnDone) {
	s.mu.Lock()
	s.active = false
	s.activeCancel = nil
	s.activeInputs = nil
	s.activeSteerRequests = nil
	pending := make([]func(error), 0, len(s.pendingSteers))
	for _, reject := range s.pendingSteers {
		pending = append(pending, reject)
	}
	s.pendingSteers = nil
	broker := s.broker
	s.broker = nil
	sessionID := s.session
	s.mu.Unlock()
	for _, reject := range pending {
		reject(errors.New("run ended before the input was durably accepted"))
	}
	if broker != nil {
		broker.Close()
	}
	if result.err != nil {
		_ = s.emit(result.id, "error", map[string]any{"message": result.err.Error(), "recoverable": true})
	}
	_ = s.emit(result.id, "turn_finished", map[string]any{"session_id": sessionID, "text": result.text})
}

type turnDone struct {
	id, text string
	err      error
}

func rpcCapabilities(steeringEnabled bool) []string {
	capabilities := []string{"attach", "cancel", "detach", "set_model"}
	if steeringEnabled {
		capabilities = append(capabilities, "steer")
	}
	return capabilities
}

func (s *rpcServer) handle(msg rpcMessage, finished chan<- turnDone) {
	s.mu.Lock()
	s.requestTypes[msg.ID] = msg.Type
	s.mu.Unlock()
	payload := map[string]json.RawMessage{}
	if len(msg.Payload) > 0 {
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid payload: " + err.Error(), "recoverable": true})
			return
		}
	}
	get := func(key string) string { var v string; _ = json.Unmarshal(payload[key], &v); return v }
	switch msg.Type {
	case "start":
		if s.started {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "RPC session already started", "recoverable": true})
			return
		}
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		workspace := get("workspace")
		if workspace == "" {
			workspace, _ = os.Getwd()
		}
		workspace, _ = filepath.Abs(workspace)
		sessionID := get("session_id")
		imageGenDriver := cfg.ImageGenDriver
		if sessionID != "" {
			imageGenDriver = ""
			savedDriver, saved, snapshotErr := runner.LoadSavedImageGenFingerprint(s.ctx, s.sessionDir, sessionID, workspace)
			if snapshotErr != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "load saved session ImageGen configuration: " + snapshotErr.Error(), "recoverable": true})
				return
			}
			if saved {
				imageGenDriver = savedDriver
			}
		}
		requestedProviderID := get("provider_id")
		forceNativeProvider := strings.EqualFold(requestedProviderID, "native")
		if forceNativeProvider {
			requestedProviderID = ""
		}
		providerID := ""
		if sessionID != "" {
			savedID, savedFingerprint, saved, snapshotErr := runner.LoadSavedProvider(s.ctx, s.sessionDir, sessionID, workspace)
			if snapshotErr != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "load saved session provider: " + snapshotErr.Error(), "recoverable": true})
				return
			}
			if saved {
				providerID = savedID
				if providerID == "" && savedFingerprint != "" {
					configured, err := rpcProviderStore().List()
					if err != nil {
						_ = s.emit(msg.ID, "error", map[string]any{"message": "load configured providers: " + err.Error(), "recoverable": true})
						return
					}
					for _, candidate := range configured {
						if candidate.Fingerprint() == savedFingerprint {
							providerID = candidate.ID
							break
						}
					}
					if providerID == "" {
						_ = s.emit(msg.ID, "error", map[string]any{"message": "this saved session uses a provider whose ID cannot be resolved; configure the original provider or start a new session", "recoverable": true})
						return
					}
				}
				if (requestedProviderID != "" && requestedProviderID != providerID) || (forceNativeProvider && providerID != "") {
					_ = s.emit(msg.ID, "error", map[string]any{"message": "the requested provider does not match this saved session; select the original provider or start a new session", "recoverable": true})
					return
				}
			} else {
				if forceNativeProvider {
					providerID = ""
				} else {
					providerID, err = resolveRPCProviderID(requestedProviderID)
				}
			}
		} else {
			if forceNativeProvider {
				providerID = ""
			} else {
				providerID, err = resolveRPCProviderID(requestedProviderID)
			}
		}
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "select model provider: " + err.Error(), "recoverable": true})
			return
		}
		provider, err := resolveRPCProvider(providerID)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "load model provider: " + err.Error(), "recoverable": true})
			return
		}
		if err := applyRPCProviderPreference(&provider); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "load provider model preference: " + err.Error(), "recoverable": true})
			return
		}
		model := get("model")
		if model == "" {
			model = provider.DefaultModel
			if model == "" {
				model = strings.TrimSpace(os.Getenv("PK_MODEL"))
			}
			if model == "" {
				model = cfg.Model
			}
		}
		effort := get("effort")
		if effort == "" {
			effort = provider.DefaultEffort
			if effort == "" {
				effort = strings.TrimSpace(os.Getenv("PK_EFFORT"))
			}
			if effort == "" {
				effort = cfg.Effort
			}
		}
		if !config.ValidEffort(effort) {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "unsupported reasoning effort " + effort + " (none is not supported by the current adapter)", "recoverable": true})
			return
		}
		effort = strings.ToLower(effort)
		if st, err := os.Stat(workspace); err != nil || !st.IsDir() {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "workspace must be an existing directory", "recoverable": true})
			return
		}
		if err := ensurePrivateDirectory(pkHome()); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		pluginPaths, pluginIssues, err := snapshotRPCPluginPaths(userPluginService())
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		for _, issue := range pluginIssues {
			fmt.Fprintf(s.diagnostics, "pk: plugin warning: %v\n", issue)
		}
		s.opts = runner.Options{Workspace: workspace, Model: model, Effort: effort, ProviderID: providerID, ImageGenFingerprint: imageGenDriver, CompactCapturedOutput: cfg.ContextPolicy == config.ContextPolicyCompact, SessionDir: s.sessionDir, SkillsDirs: defaultSkillDirs(), ToolEvents: true}
		if err := applyConfiguredContextManagement(&s.opts, cfg, provider.BaseURL); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "resolve context budget: " + err.Error(), "recoverable": true})
			return
		}
		s.providerID = providerID
		s.contextBudgetConfig = cfg.ContextBudget
		s.historyCompactionConfig = cfg.HistoryCompaction
		s.session = sessionID
		s.opts.SessionID = s.session
		s.pluginPaths = pluginPaths
		s.pluginIssues = pluginIssueMessages(pluginIssues)
		var steeringEnabled bool
		_ = json.Unmarshal(payload["steering"], &steeringEnabled)
		s.steeringEnabled = steeringEnabled
		var providerReasoningRequested bool
		_ = json.Unmarshal(payload["provider_reasoning"], &providerReasoningRequested)
		s.providerReasoningRequested = providerReasoningRequested
		s.providerReasoningEnabled = providerReasoningRequested && (provider.Protocol == providers.ProtocolChatCompletions || provider.Protocol == providers.ProtocolCloudflareWorkersAI)
		if s.session != "" {
			if err := s.emitSessionHistory(msg.ID, s.session); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
				return
			}
		}
		s.started = true
		capabilities := rpcCapabilities(steeringEnabled)
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": s.session, "model": model, "effort": effort, "provider_id": providerID, "imagegen_enabled": imageGenDriver != "", "imagegen_driver": imageGenDriver, "provider_reasoning_enabled": s.providerReasoningEnabled, "capabilities": capabilities, "release_status": rpcCurrentReleaseStatus()})
		if s.session != "" {
			_ = s.emit(msg.ID, "session", map[string]any{"session_id": s.session})
		}
	case "prompt":
		text := get("text")
		if strings.TrimSpace(text) == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "prompt text is empty", "recoverable": true})
			return
		}
		originalUserText := text
		var files []string
		if raw := payload["files"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &files); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "files must be an array of paths: " + err.Error(), "recoverable": true})
				return
			}
		}
		s.mu.Lock()
		if !s.started {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before prompt", "recoverable": true})
			return
		}
		if s.active || s.pluginCommandActive || s.skillOperationActive {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "a foreground operation is already running", "recoverable": true})
			return
		}
		if s.reloadPrepared {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "reload is being prepared", "recoverable": true})
			return
		}
		if s.releaseActive {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "an update or rollback is running", "recoverable": true})
			return
		}
		if len(s.pluginIssues) > 0 {
			issues := append([]string(nil), s.pluginIssues...)
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": rpcPluginUnavailableMessage(issues), "recoverable": true})
			return
		}
		workspace, sessionID, providerID := s.opts.Workspace, s.session, s.providerID
		model := s.opts.Model
		if providerID == "" && isCloudflareWorkersAIModel(model) {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": nativeCloudflareModelError().Error(), "recoverable": true})
			return
		}
		contextConfig, historyConfig := s.contextBudgetConfig, s.historyCompactionConfig
		pluginPaths := append([]string(nil), s.pluginPaths...)
		ctx, cancel := context.WithCancel(s.ctx)
		s.activeCancel = cancel
		s.active = true
		var inputStream chan runner.Input
		var steeringRequests chan steeringRequest
		var sessionReady chan string
		steeringEnabled := s.steeringEnabled
		providerReasoningEnabled := s.providerReasoningEnabled
		ctx = modelstream.WithProviderReasoning(ctx, providerReasoningEnabled)
		if steeringEnabled {
			inputStream = make(chan runner.Input, 64)
			steeringRequests = make(chan steeringRequest, 64)
			sessionReady = make(chan string, 1)
			s.activeInputs = inputStream
			s.activeSteerRequests = steeringRequests
		} else {
			s.activeInputs = nil
			s.activeSteerRequests = nil
		}
		broker := interaction.NewBroker(ctx, sessionID)
		s.broker = broker
		opts := s.opts
		opts.SessionID = sessionID
		var promptID, pageDir string
		var cleanupPages func()
		if steeringEnabled {
			opts.Inputs = inputStream
			opts.QueueInputs = true
		}
		s.mu.Unlock()
		_ = s.emit(msg.ID, "turn_started", map[string]any{"session_id": sessionID})
		go func() {
			for {
				select {
				case question := <-broker.Questions():
					if broker.Context().Err() != nil {
						return
					}
					_ = s.emit(msg.ID, "question", question)
				case <-broker.Context().Done():
					return
				}
			}
		}()
		go func() {
			defer cancel()
			if steeringRequests != nil {
				go s.prepareSteeringInputs(ctx, steeringRequests, inputStream, workspace, s.sessionDir, sessionID, sessionReady)
			}
			pagesPersisted := false
			if len(files) > 0 {
				var identityErr error
				promptID, pageDir, cleanupPages, identityErr = prepareAttachmentIdentity(&opts)
				if identityErr != nil {
					finished <- turnDone{id: msg.ID, err: fmt.Errorf("prepare attachment storage: %w", identityErr)}
					return
				}
				opts.PromptID = promptID
				defer func() {
					if !pagesPersisted {
						cleanupPages()
					}
				}()
				opts.AfterInputPersist = func(string, string) { pagesPersisted = true }
			}
			var loadedAttachments []attachments.Attachment
			if err := ctx.Err(); err != nil {
				finished <- turnDone{id: msg.ID, err: err}
				return
			}
			if len(files) > 0 {
				resolved, err := workspaceAttachmentPaths(workspace, files)
				if err != nil {
					finished <- turnDone{id: msg.ID, err: err}
					return
				}
				if loader := s.loadAttachments; loader != nil {
					text, loadedAttachments, err = loader(ctx, workspace, text, resolved)
				} else {
					text, loadedAttachments, err = loadPromptAttachmentsWithPageDir(ctx, workspace, text, resolved, pageDir)
				}
				if err != nil {
					finished <- turnDone{id: msg.ID, err: fmt.Errorf("load attachments: %w", err)}
					return
				}
				if err := ctx.Err(); err != nil {
					finished <- turnDone{id: msg.ID, err: err}
					return
				}
				if cleanupPages != nil && !hasRenderedPDFPages(loadedAttachments) {
					cleanupPages()
					pagesPersisted = true
				}
				summaries := make([]map[string]any, 0, len(loadedAttachments))
				for _, item := range loadedAttachments {
					summaries = append(summaries, map[string]any{"path": item.Path, "kind": item.Kind, "content_type": item.ContentType, "truncated": item.Truncated, "pages_extracted": item.PagesExtracted, "pages_total": item.PagesTotal, "rendered_pages": len(item.RenderedPDFPages), "render_notice": item.RenderNotice})
				}
				_ = s.emit(msg.ID, "attachments_loaded", map[string]any{"files": summaries})
			}
			if err := ctx.Err(); err != nil {
				finished <- turnDone{id: msg.ID, err: err}
				return
			}
			var client llm.Adapter
			var selectedProvider *providers.Provider
			if providerID != "" {
				provider, err := resolveRPCProvider(providerID)
				if err != nil {
					finished <- turnDone{id: msg.ID, err: fmt.Errorf("load selected provider: %w", err)}
					return
				}
				providerClient, err := providers.NewClient(provider)
				if err != nil {
					finished <- turnDone{id: msg.ID, err: fmt.Errorf("prepare selected provider: %w", err)}
					return
				}
				client = providerClient
				selectedProvider = &provider
				opts.ProviderFingerprint = provider.Fingerprint()
				defer func() {
					if closeErr := providerClient.Close(); closeErr != nil {
						fmt.Fprintf(s.diagnostics, "pk: close provider client: %v\n", closeErr)
					}
				}()
			} else {
				s.mu.Lock()
				defaultClient := s.adapter
				s.mu.Unlock()
				if defaultClient == nil {
					prepare := s.prepareAdapter
					if prepare == nil {
						prepare = prepareAdapter
					}
					var err error
					defaultClient, err = prepare(ctx, s.useCodex, s.codexPath)
					if err != nil {
						finished <- turnDone{id: msg.ID, err: err}
						return
					}
					s.mu.Lock()
					if s.adapter == nil {
						s.adapter = defaultClient
					} else if defaultClient != s.adapter {
						_ = defaultClient.Close()
						defaultClient = s.adapter
					}
					s.mu.Unlock()
				}
				client = defaultClient
				opts.ProviderFingerprint = ""
			}
			if err := ctx.Err(); err != nil {
				finished <- turnDone{id: msg.ID, err: err}
				return
			}
			s.mu.Lock()
			opts.SessionID = s.session
			opts.ProviderID = providerID
			opts.Adapter = client
			s.mu.Unlock()
			out := &rpcRunnerOutput{server: s, id: msg.ID}
			opts.Prompt = text
			if len(loadedAttachments) > 0 {
				if record, recordErr := presentation.NewRecord(originalUserText, text, loadedAttachments); recordErr != nil {
					fmt.Fprintf(s.diagnostics, "pk: could not describe attachment history: %v\n", recordErr)
				} else {
					opts.BeforeInputPersist = func(sessionID, inputID string) error {
						if inputID != promptID {
							fmt.Fprintln(s.diagnostics, "pk: attachment presentation input ID mismatch; full prompt will be retained")
							return nil
						}
						saveHistoryPresentation(s.sessionDir, sessionID, inputID, record, s.diagnostics)
						return nil
					}
				}
			}
			opts.Output = out
			opts.JSONL = true
			opts.Diagnostics = s.diagnostics
			opts.Adapter = modelProgressAdapter{inner: client, observe: func(progress modelstream.Event) {
				if ctx.Err() != nil {
					return
				}
				payload := map[string]any{
					"request_id": progress.RequestID,
					"attempt":    progress.Attempt,
					"status":     progress.Status,
					"phase":      progress.Kind,
				}
				if progress.Text != "" {
					payload["text_delta"] = progress.Text
				}
				if progress.ItemID != "" {
					payload["item_id"] = progress.ItemID
				}
				if progress.ToolName != "" {
					payload["tool_name"] = progress.ToolName
				}
				if progress.Bytes != 0 {
					payload["bytes"] = progress.Bytes
				}
				_ = s.emit(msg.ID, "model_progress", payload)
			}}
			opts.OnSession = func(id string) {
				s.mu.Lock()
				s.session = id
				s.opts.SessionID = id
				s.mu.Unlock()
				if sessionReady != nil {
					select {
					case sessionReady <- id:
					default:
					}
				}
				_ = s.emit(msg.ID, "session", map[string]any{"session_id": id})
			}
			additional := []cliRegistryExtension{tinyFishRegistryExtension(broker.Context())}
			if opts.ImageGenFingerprint != "" {
				imageConfig := imagegen.Config{Driver: opts.ImageGenFingerprint, Effort: "low", CodexHome: strings.TrimSpace(os.Getenv("CODEX_HOME"))}
				additional = append(additional, cliRegistryExtension{Decorate: imagegen.Decorator(imageConfig, opts.Workspace), RemoteJobHandlers: imagegen.HandlerFactory(imageConfig, opts.Workspace)})
			}
			host, err := configureRPCPluginSession(broker.Context(), &opts, pluginPaths, broker, s.diagnostics, additional...)
			if err != nil {
				finished <- turnDone{id: msg.ID, err: fmt.Errorf("load session plugins: %w", err)}
				return
			}
			stopPluginProgress := relayRPCPluginProgress(broker.Context(), s, host, msg.ID)
			defer stopPluginProgress()
			finishPluginSchema, err := prepareRPCPluginSession(&opts, host)
			if err != nil {
				if host != nil {
					_ = host.Close()
				}
				finished <- turnDone{id: msg.ID, err: err}
				return
			}
			mcpHost, err := configureCLIMCP(broker.Context(), &opts, s.diagnostics)
			if err != nil {
				if host != nil {
					_ = host.Close()
				}
				finished <- turnDone{id: msg.ID, err: fmt.Errorf("load configured MCP servers: %w", err)}
				return
			}
			mcpServers, err := configuredSubagentMCPServers()
			if err != nil {
				if mcpHost != nil {
					_ = mcpHost.Close()
				}
				if host != nil {
					_ = host.Close()
				}
				finished <- turnDone{id: msg.ID, err: fmt.Errorf("load MCP configuration for subagents: %w", err)}
				return
			}
			subagentManager, err := configureSubagents(broker.Context(), &opts, subagentRuntimeConfig{
				Workspace: opts.Workspace, SessionDir: opts.SessionDir, SkillsDirs: opts.SkillsDirs,
				PluginManifests: pluginPaths, MCPServers: mcpServers,
				InheritPlugins: len(pluginPaths) > 0, InheritMCP: len(mcpServers) > 0,
				ProviderID: providerID, ProviderConfig: selectedProvider,
				ContextBudgetConfig: contextConfig, HistoryCompactionConfig: historyConfig,
				UseCodex: s.useCodex, CodexPath: s.codexPath, Diagnostics: s.diagnostics,
				AdapterFactory: func(adapterCtx context.Context, useCodex bool, codexPath string) (llm.Adapter, error) {
					if selectedProvider != nil {
						return providers.NewClient(*selectedProvider)
					}
					prepare := s.prepareAdapter
					if prepare == nil {
						prepare = prepareAdapter
					}
					return prepare(adapterCtx, useCodex, codexPath)
				},
				Events: func(event subagents.Event) { _ = s.emit(msg.ID, "subagent", event) },
			})
			if err != nil {
				if mcpHost != nil {
					_ = mcpHost.Close()
				}
				if host != nil {
					_ = host.Close()
				}
				finished <- turnDone{id: msg.ID, err: fmt.Errorf("configure subagents: %w", err)}
				return
			}
			result, err := runner.Run(broker.Context(), opts)
			subagentManager.Close()
			if mcpHost != nil {
				if closeErr := mcpHost.Close(); err == nil && closeErr != nil {
					err = closeErr
				}
			}
			if host != nil {
				if closeErr := host.Close(); err == nil && closeErr != nil {
					err = closeErr
				}
			}
			if schemaErr := finishPluginSchema(); err == nil && schemaErr != nil {
				err = schemaErr
			}
			finished <- turnDone{id: msg.ID, text: result.Text, err: err}
		}()
	case "clipboard_paste":
		s.mu.Lock()
		started, workspace := s.started, s.opts.Workspace
		s.mu.Unlock()
		if !started {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before requesting clipboard data", "recoverable": true})
			return
		}
		provider := s.clipboardProvider
		if provider == nil {
			provider = clipboard.NativeProvider{}
		}
		s.startClipboardOperation(msg.ID, false, func(ctx context.Context) (string, any, error) {
			snapshot, err := provider.Read(ctx)
			if err != nil {
				return "clipboard_files", map[string]any{"files": []clipboard.SelectedFile{}, "text": "", "message": err.Error()}, nil
			}
			if err := ctx.Err(); err != nil {
				return "clipboard_files", map[string]any{"files": []clipboard.SelectedFile{}, "text": "", "message": err.Error()}, nil
			}
			snapshot = clipboard.NormalizeSnapshot(snapshot)
			selected, err := clipboard.CaptureSnapshot(ctx, snapshot, filepath.Join(pkHome(), "attachments"))
			if err != nil {
				return "clipboard_files", map[string]any{"files": []clipboard.SelectedFile{}, "text": snapshot.Text, "message": err.Error()}, nil
			}
			// The UI retains these explicit selections and submits their paths through
			// the same bounded attachment loader used by --file and ordinary prompts.
			return "clipboard_files", map[string]any{"files": selected, "text": snapshot.Text, "workspace": workspace, "message": snapshot.Message}, nil
		})
	case "clipboard_write":
		var text string
		if err := json.Unmarshal(payload["text"], &text); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "clipboard_write requires a text string", "recoverable": true})
			return
		}
		if err := clipboard.ValidateText(text); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		s.mu.Lock()
		started := s.started
		s.mu.Unlock()
		if !started {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before writing clipboard text", "recoverable": true})
			return
		}
		writer := s.clipboardWriter
		if writer == nil {
			writer = clipboard.NativeWriter{}
		}
		s.startClipboardOperation(msg.ID, true, func(ctx context.Context) (string, any, error) {
			if err := writer.WriteText(ctx, text); err != nil {
				return "", nil, err
			}
			return "clipboard_written", map[string]any{"bytes": len(text)}, nil
		})
	case "cancel":
		s.mu.Lock()
		cancel, active, sessionID, model, effort := s.activeCancel, s.active, s.session, s.opts.Model, s.opts.Effort
		s.activeInputs = nil
		s.activeSteerRequests = nil
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		_ = s.emit(msg.ID, "status", map[string]any{"session_id": sessionID, "model": model, "effort": effort, "cancel_requested": active})
	case "update":
		sourcePath := get("source_path")
		s.startReleaseOperation(msg.ID, "update", sourcePath)
	case "rollback":
		s.startReleaseOperation(msg.ID, "rollback", "")
	case "update_cancel":
		s.mu.Lock()
		cancel, active := s.releaseCancel, s.releaseActive
		s.mu.Unlock()
		if !active || cancel == nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "no update or rollback is running", "recoverable": true})
			return
		}
		cancel()
		_ = s.emit(msg.ID, "update_cancel_requested", map[string]any{"operation": "release"})
	case "reload":
		s.mu.Lock()
		busy := s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared
		providerID := s.providerID
		if providerID == "" {
			providerID = "native"
		}
		handoff := reloadHandoff{Workspace: s.opts.Workspace, SessionID: s.session, Model: s.opts.Model, Effort: s.opts.Effort, ProviderID: providerID}
		started := s.started
		if started && !busy {
			s.reloadPrepared = true // reserve the idle lifecycle while the handoff is written
		}
		s.mu.Unlock()
		if !started {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before reloading", "recoverable": true})
			return
		}
		if busy {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "reload requires an idle session with no attached task", "recoverable": true})
			return
		}
		if err := writeReloadHandoff(handoff); err != nil {
			s.mu.Lock()
			s.reloadPrepared = false
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "reload_ready", map[string]any{"session_id": handoff.SessionID})
	case "reload_exit":
		s.mu.Lock()
		prepared := s.reloadPrepared
		if prepared {
			s.quit = true
		}
		s.mu.Unlock()
		if !prepared {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "reload handoff is not prepared", "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "reload_exit", map[string]any{"ready": true})
	case "steer":
		text := get("text")
		var files []string
		if raw, present := payload["files"]; present && len(raw) > 0 {
			if err := json.Unmarshal(raw, &files); err != nil {
				s.rejectSteer(msg.ID, "files must be an array of paths: "+err.Error())
				return
			}
		}
		if strings.TrimSpace(text) == "" && len(files) == 0 {
			s.rejectSteer(msg.ID, "steering text or at least one attachment is required")
			return
		}
		if len(files) > attachments.DefaultLimits().MaxFiles {
			s.rejectSteer(msg.ID, fmt.Sprintf("too many steering attachments: got %d, limit %d", len(files), attachments.DefaultLimits().MaxFiles))
			return
		}
		if strings.TrimSpace(msg.ID) == "" {
			s.rejectSteer(msg.ID, "steering request ID is required")
			return
		}
		s.mu.Lock()
		steeringEnabled := s.steeringEnabled
		s.mu.Unlock()
		if !steeringEnabled {
			s.rejectSteer(msg.ID, "foreground steering was not enabled at session start")
			return
		}
		queuedEvent := make(chan struct{})
		inputID, requestID := msg.ID, msg.ID
		artifacts := &steeringArtifactCleanup{}
		var replyOnce sync.Once
		reply := func(acceptedErr error) {
			replyOnce.Do(func() {
				<-queuedEvent
				if acceptedErr != nil {
					artifacts.run()
				}
				s.mu.Lock()
				delete(s.pendingSteers, inputID)
				sessionID := s.session
				s.mu.Unlock()
				if acceptedErr != nil {
					_ = s.emit(requestID, "input_rejected", map[string]any{"input_id": inputID, "session_id": sessionID, "message": acceptedErr.Error()})
					return
				}
				_ = s.emit(requestID, "input_accepted", map[string]any{"input_id": inputID, "session_id": sessionID})
			})
		}
		s.mu.Lock()
		requestQueue := s.activeSteerRequests
		sessionID := s.session
		if !s.active || s.activeInputs == nil || requestQueue == nil {
			s.mu.Unlock()
			close(queuedEvent)
			s.rejectSteer(msg.ID, "there is no active foreground run to steer")
			return
		}
		if s.pendingSteers == nil {
			s.pendingSteers = make(map[string]func(error))
		}
		if _, duplicate := s.pendingSteers[inputID]; duplicate {
			s.mu.Unlock()
			close(queuedEvent)
			s.rejectSteer(msg.ID, "steering request ID is already pending")
			return
		}
		s.pendingSteers[inputID] = reply
		select {
		case requestQueue <- steeringRequest{requestID: requestID, inputID: inputID, text: text, files: append([]string(nil), files...), reply: reply, artifacts: artifacts}:
			s.mu.Unlock()
			_ = s.emit(msg.ID, "input_queued", map[string]any{"input_id": inputID, "session_id": sessionID, "attachments": len(files)})
			close(queuedEvent)
		default:
			delete(s.pendingSteers, inputID)
			s.mu.Unlock()
			close(queuedEvent)
			s.rejectSteer(msg.ID, "interactive input queue is full; message and files were not queued")
		}
	case "answer_question":
		var questionID, answer string
		_ = json.Unmarshal(payload["id"], &questionID)
		_ = json.Unmarshal(payload["answer"], &answer)
		s.mu.Lock()
		broker := s.broker
		s.mu.Unlock()
		if err := broker.Answer(questionID, answer); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "question_answered", map[string]any{"id": questionID})
	case "cancel_question":
		var questionID string
		_ = json.Unmarshal(payload["id"], &questionID)
		s.mu.Lock()
		broker, cancel := s.broker, s.activeCancel
		s.mu.Unlock()
		if err := broker.Cancel(questionID); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		if cancel != nil {
			cancel()
		}
		_ = s.emit(msg.ID, "question_cancelled", map[string]any{"id": questionID})
	case "attach":
		s.mu.Lock()
		active := s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive
		s.mu.Unlock()
		if active {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cannot attach while a turn is running", "recoverable": true})
			return
		}
		id := get("session_id")
		if id == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "session_id is required", "recoverable": true})
			return
		}
		s.mu.Lock()
		currentSessionID := s.session
		s.mu.Unlock()
		saved, err := s.getSession(s.ctx, id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "could not load saved session metadata: " + err.Error(), "recoverable": true})
			return
		}
		if saved.Active && id != currentSessionID {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "that session is active in another pk process; wait until it finishes before attaching", "recoverable": true})
			return
		}
		if strings.TrimSpace(saved.Workspace) == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "the saved session has no workspace metadata; start pk in its original workspace to attach", "recoverable": true})
			return
		}
		workspace, err := filepath.Abs(saved.Workspace)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "could not resolve saved session workspace", "recoverable": true})
			return
		}
		savedImageDriver, hasImageSnapshot, imageErr := runner.LoadSavedImageGenFingerprint(s.ctx, s.sessionDir, id, workspace)
		if imageErr != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "load saved session ImageGen configuration: " + imageErr.Error(), "recoverable": true})
			return
		}
		workspaceInfo, err := os.Stat(workspace)
		if err != nil || !workspaceInfo.IsDir() {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "saved session workspace is unavailable; restore that directory before attaching", "recoverable": true})
			return
		}
		pluginPaths, pluginIssues, err := snapshotRPCPluginPaths(userPluginService())
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		for _, issue := range pluginIssues {
			fmt.Fprintf(s.diagnostics, "pk: plugin warning: %v\n", issue)
		}
		s.mu.Lock()
		s.session = id
		s.opts.SessionID = id
		s.pluginPaths = pluginPaths
		s.pluginIssues = pluginIssueMessages(pluginIssues)
		s.opts.Workspace = workspace
		if hasImageSnapshot {
			s.opts.ImageGenFingerprint = savedImageDriver
		} else {
			s.opts.ImageGenFingerprint = ""
		}
		workspace, model, effort := s.opts.Workspace, s.opts.Model, s.opts.Effort
		imageGenDriver := s.opts.ImageGenFingerprint
		steeringEnabled, providerID := s.steeringEnabled, s.providerID
		providerReasoningEnabled := s.providerReasoningEnabled
		s.mu.Unlock()
		if err := s.emitSessionHistory(msg.ID, id); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "session", map[string]any{"session_id": id})
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": id, "model": model, "effort": effort, "provider_id": providerID, "imagegen_enabled": imageGenDriver != "", "imagegen_driver": imageGenDriver, "attached": true, "provider_reasoning_enabled": providerReasoningEnabled, "capabilities": rpcCapabilities(steeringEnabled), "release_status": rpcCurrentReleaseStatus()})
	case "new":
		s.mu.Lock()
		active, sessionID := s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive, s.session
		s.mu.Unlock()
		if active {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cancel the active turn before starting a new session", "recoverable": true})
			return
		}
		pluginPaths, pluginIssues, err := snapshotRPCPluginPaths(userPluginService())
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		for _, issue := range pluginIssues {
			fmt.Fprintf(s.diagnostics, "pk: plugin warning: %v\n", issue)
		}
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "load pk defaults: " + err.Error(), "recoverable": true})
			return
		}
		s.mu.Lock()
		s.session = ""
		s.opts.SessionID = ""
		s.pluginPaths = pluginPaths
		s.pluginIssues = pluginIssueMessages(pluginIssues)
		s.opts.ImageGenFingerprint = cfg.ImageGenDriver
		workspace, model, effort := s.opts.Workspace, s.opts.Model, s.opts.Effort
		imageGenDriver := s.opts.ImageGenFingerprint
		steeringEnabled, providerID := s.steeringEnabled, s.providerID
		providerReasoningEnabled := s.providerReasoningEnabled
		s.mu.Unlock()
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": "", "previous_session_id": sessionID, "model": model, "effort": effort, "provider_id": providerID, "imagegen_enabled": imageGenDriver != "", "imagegen_driver": imageGenDriver, "provider_reasoning_enabled": providerReasoningEnabled, "capabilities": rpcCapabilities(steeringEnabled), "release_status": rpcCurrentReleaseStatus()})
	case "history_before":
		var request struct {
			SessionID      string `json:"session_id"`
			BeforeSequence uint64 `json:"before_sequence"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil || request.SessionID == "" {
			if err == nil {
				err = errors.New("session_id is required")
			}
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid history cursor: " + err.Error(), "recoverable": true})
			return
		}
		s.mu.Lock()
		currentSession, sessionDir := s.session, s.sessionDir
		s.mu.Unlock()
		if currentSession == "" || request.SessionID != currentSession {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "history cursor belongs to a different or no-longer-active session", "recoverable": true})
			return
		}
		s.startSkillOperation(msg.ID, "history page", "history_page_started", "history_page", map[string]any{"session_id": request.SessionID}, func(ctx context.Context) (any, error) {
			store, err := localfile.New(sessionDir)
			if err != nil {
				return nil, err
			}
			entries, hasEarlier, truncated, beforeSequence, err := sessionHistoryPage(ctx, store, request.SessionID, request.BeforeSequence, s.sessionDir)
			if err != nil {
				return nil, err
			}
			return map[string]any{"session_id": request.SessionID, "entries": entries, "has_earlier": hasEarlier, "before_sequence": beforeSequence, "truncated": truncated}, nil
		})
	case "history_cancel":
		s.cancelRPCOperation(msg.ID, "history page", "history_cancel_requested")
	case "session_usage_cancel":
		var request struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil || strings.TrimSpace(request.RequestID) == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "session_usage_cancel requires request_id", "recoverable": true})
			return
		}
		s.cancelRPCOperationByID(msg.ID, request.RequestID, "session usage", "session_usage_cancel_requested")
	case "session_usage":
		var request struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil || strings.TrimSpace(request.SessionID) == "" {
			if err == nil {
				err = errors.New("session_id is required")
			}
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid session usage request: " + err.Error(), "recoverable": true})
			return
		}
		s.mu.Lock()
		sessionDir := s.sessionDir
		s.mu.Unlock()
		s.startSkillOperation(msg.ID, "session usage", "session_usage_started", "session_usage", map[string]any{"session_id": request.SessionID}, func(ctx context.Context) (any, error) {
			store, err := localfile.New(sessionDir)
			if err != nil {
				return nil, err
			}
			return readSessionUsage(ctx, store, request.SessionID, sessionDir)
		})
	case "plugins_list":
		payload, err := listRPCPlugins(userPluginService())
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "plugins", payload)
	case "plugin_commands_list":
		s.mu.Lock()
		started := s.started
		manifestPaths := append([]string(nil), s.pluginPaths...)
		s.mu.Unlock()
		if !started {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before listing session plugin commands", "recoverable": true})
			return
		}
		commands, issues := pluginSlashCommandCatalog(manifestPaths)
		publicCommands := make([]map[string]string, 0, len(commands))
		for _, command := range commands {
			publicCommands = append(publicCommands, map[string]string{
				"name": command.Name, "extension_id": command.ExtensionID,
				"command_name": command.CommandName, "description": command.Description,
			})
		}
		publicIssues := make([]string, 0, len(issues))
		for _, issue := range issues {
			publicIssues = append(publicIssues, issue.Error())
		}
		_ = s.emit(msg.ID, "plugin_commands", map[string]any{"commands": publicCommands, "issues": publicIssues})
	case "plugin_command_execute":
		name, arguments := get("name"), get("arguments")
		if name == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "plugin command name is required", "recoverable": true})
			return
		}
		if len(arguments) > extensions.MaxSlashCommandArgumentBytes || !utf8.ValidString(arguments) {
			_ = s.emit(msg.ID, "error", map[string]any{"message": fmt.Sprintf("plugin command arguments must be valid UTF-8 and at most %d bytes", extensions.MaxSlashCommandArgumentBytes), "recoverable": true})
			return
		}
		s.mu.Lock()
		if !s.started {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before running a plugin command", "recoverable": true})
			return
		}
		if s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "plugin commands require an idle session with no attached task", "recoverable": true})
			return
		}
		workspace := s.opts.Workspace
		manifestPaths := append([]string(nil), s.pluginPaths...)
		ctx, cancel := context.WithCancel(s.ctx)
		done := make(chan struct{})
		s.pluginCommandActive = true
		s.pluginCommandCancel = cancel
		s.pluginCommandDone = done
		s.mu.Unlock()
		_ = s.emit(msg.ID, "plugin_command_started", map[string]any{"name": name})
		go func() {
			text, err := executePluginSlashCommand(ctx, workspace, manifestPaths, name, arguments)
			wasCanceled := ctx.Err() != nil
			cancel()
			s.mu.Lock()
			s.pluginCommandActive = false
			s.pluginCommandCancel = nil
			s.mu.Unlock()
			if err != nil {
				payload := map[string]any{"message": err.Error(), "recoverable": true, "command": name}
				if wasCanceled {
					payload["cancelled"] = true
				}
				_ = s.emit(msg.ID, "error", payload)
			} else {
				_ = s.emit(msg.ID, "plugin_command_result", map[string]any{"name": name, "text": text})
			}
			close(done)
			s.mu.Lock()
			if s.pluginCommandDone == done {
				s.pluginCommandDone = nil
			}
			s.mu.Unlock()
		}()
	case "plugin_command_cancel":
		s.mu.Lock()
		cancel, active := s.pluginCommandCancel, s.pluginCommandActive
		s.mu.Unlock()
		if !active || cancel == nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "no plugin command is running", "recoverable": true})
			return
		}
		cancel()
		_ = s.emit(msg.ID, "plugin_command_cancel_requested", map[string]any{})
	case "image_status":
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "load pk defaults: " + err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "image_status", map[string]any{"enabled": cfg.ImageGenDriver != "", "driver": cfg.ImageGenDriver})
	case "image_configure":
		var request struct {
			Enabled bool   `json:"enabled"`
			Driver  string `json:"driver,omitempty"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid ImageGen configuration", "recoverable": true})
			return
		}
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "load pk defaults: " + err.Error(), "recoverable": true})
			return
		}
		if request.Enabled {
			driver := strings.TrimSpace(request.Driver)
			if driver == "" {
				driver = cfg.ImageGenDriver
			}
			if driver == "" {
				driver = config.DefaultImageGenDriver
			}
			if !config.ValidImageGenDriver(driver) {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid ImageGen driver; use a model ID without whitespace", "recoverable": true})
				return
			}
			cfg.ImageGenDriver = driver
		} else {
			cfg.ImageGenDriver = ""
		}
		if err := config.Save(s.cfgPath, cfg); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "save ImageGen configuration: " + err.Error(), "recoverable": true})
			return
		}
		s.mu.Lock()
		if s.session == "" && !s.active && s.attachedTask == "" {
			s.opts.ImageGenFingerprint = cfg.ImageGenDriver
		}
		s.mu.Unlock()
		_ = s.emit(msg.ID, "image_configured", map[string]any{"enabled": cfg.ImageGenDriver != "", "driver": cfg.ImageGenDriver, "next_session_only": true})
	case "providers_list":
		items, err := rpcProviderStore().Summaries()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "providers", map[string]any{"providers": items})
	case "release_status":
		_ = s.emit(msg.ID, "release_status", rpcCurrentReleaseStatus())
	case "provider_models":
		providerID := get("provider_id")
		if providerID == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "provider_id is required", "recoverable": true})
			return
		}
		provider, err := resolveRPCProvider(providerID)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		key, err := provider.APIKeyValue()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		provider.APIKey, provider.APIKeyEnv = key, ""
		s.startProviderModels(providerModelsRequest{RequestID: msg.ID, Provider: provider})
	case "provider_models_cancel":
		if err := s.cancelProviderModels(get("request_id")); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "provider_models_cancelled", map[string]any{"request_id": get("request_id")})
	case "context_budget_status":
		s.handleContextBudgetStatus(msg.ID)
	case "context_budget_configure":
		s.handleContextBudgetConfigure(msg.ID, msg.Payload)
	case "compact":
		s.startManualContextCompaction(msg.ID)
	case "provider_presets_list":
		_ = s.emit(msg.ID, "provider_presets", map[string]any{"presets": rpcProviderPresets()})
	case "provider_preset_add":
		request, err := decodeProviderPresetAdd(msg.Payload)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid provider preset request", "recoverable": true})
			return
		}
		id := strings.TrimSpace(request.ID)
		if id == "" {
			id = strings.TrimSpace(request.PresetID)
		}
		if err := s.providerMutationAllowed(id); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		store := rpcProviderStore()
		provider, err := putRPCProviderPresetWithAccount(store, request.PresetID, id, request.APIKey, request.AccountID, request.DefaultModel, request.DefaultEffort)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		updated, err := rpcProvidersUpdated()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		updated.AddedProviderID = provider.ID
		updated.PresetID = request.PresetID
		_ = s.emit(msg.ID, "providers_updated", updated)
	case "provider_add":
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(msg.Payload, &raw); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid provider configuration: " + err.Error(), "recoverable": true})
			return
		}
		for key := range raw {
			if strings.EqualFold(key, "api_key") {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "literal API keys cannot be sent over this route; use a preset setup action or configure api_key_env", "recoverable": true})
				return
			}
		}
		var provider providers.Provider
		if err := json.Unmarshal(msg.Payload, &provider); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid provider configuration: " + err.Error(), "recoverable": true})
			return
		}
		if err := s.providerMutationAllowed(provider.ID); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		store := rpcProviderStore()
		if err := preserveProviderCredentials(store, &provider); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		if err := store.Put(provider); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		updated, err := rpcProvidersUpdated()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "providers_updated", updated)
	case "provider_remove":
		providerID := get("id")
		if providerID == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "provider id is required", "recoverable": true})
			return
		}
		if err := s.providerMutationAllowed(providerID); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		if err := rpcProviderStore().Remove(providerID); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		updated, err := rpcProvidersUpdated()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "providers_updated", updated)
	case "provider_default":
		providerID := get("provider_id")
		if err := s.providerMutationAllowed(""); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		if err := rpcProviderStore().SetDefault(providerID); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		updated, err := rpcProvidersUpdated()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "providers_updated", updated)
	case "provider_select":
		providerID := strings.TrimSpace(get("provider_id"))
		if strings.EqualFold(providerID, "native") {
			providerID = ""
		}
		s.mu.Lock()
		if !s.started {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before selecting a provider", "recoverable": true})
			return
		}
		if s.active || s.session != "" || s.attachedTask != "" {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "provider selection is available only before the first prompt in a new session", "recoverable": true})
			return
		}
		s.mu.Unlock()
		selected, err := resolveRPCProvider(providerID)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		if err := applyRPCProviderPreference(&selected); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "load provider model preference: " + err.Error(), "recoverable": true})
			return
		}
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		model, effort := selected.DefaultModel, selected.DefaultEffort
		if model == "" {
			model = cfg.Model
		}
		if effort == "" {
			effort = cfg.Effort
		}
		if modelOverride := strings.TrimSpace(get("model")); modelOverride != "" {
			if len(modelOverride) > 256 || strings.ContainsAny(modelOverride, "\r\n\x00") {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "model ID must be at most 256 bytes and contain no control characters", "recoverable": true})
				return
			}
			model = modelOverride
		}
		if providerID == "" && isCloudflareWorkersAIModel(model) {
			if strings.TrimSpace(get("model")) != "" {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "a Workers AI model cannot be selected with Native Codex; choose a native model or select Cloudflare Workers AI", "recoverable": true})
				return
			}
			model = config.DefaultModel
		}
		selectedOptions := runner.Options{ProviderID: providerID, Model: model, Effort: effort}
		if err := applyConfiguredContextManagement(&selectedOptions, cfg, selected.BaseURL); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "resolve context budget: " + err.Error(), "recoverable": true})
			return
		}
		s.mu.Lock()
		if s.active || s.session != "" || s.attachedTask != "" {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "provider selection is available only before the first prompt in a new session", "recoverable": true})
			return
		}
		store := rpcProviderStore()
		if providerID == "" {
			cfg.Model, cfg.Effort = model, effort
			if err := config.Save(s.cfgPath, cfg); err != nil {
				s.mu.Unlock()
				_ = s.emit(msg.ID, "error", map[string]any{"message": "could not save native model defaults: " + err.Error(), "recoverable": true})
				return
			}
			if err := store.SetDefault(""); err != nil {
				s.mu.Unlock()
				_ = s.emit(msg.ID, "error", map[string]any{"message": "could not select Native Codex as the default provider: " + err.Error(), "recoverable": true})
				return
			}
		} else {
			var selectedEffort *string
			if selected.SupportsReasoningEffort {
				selectedEffort = &effort
			}
			if err := store.SetDefaultModelAndProvider(providerID, model, selectedEffort); err != nil {
				s.mu.Unlock()
				_ = s.emit(msg.ID, "error", map[string]any{"message": "could not save provider defaults: " + err.Error(), "recoverable": true})
				return
			}
		}
		s.providerID, s.opts.ProviderID, s.opts.Model, s.opts.Effort = providerID, providerID, model, effort
		s.providerReasoningEnabled = s.providerReasoningRequested && (selected.Protocol == providers.ProtocolChatCompletions || selected.Protocol == providers.ProtocolCloudflareWorkersAI)
		s.opts.ContextBudget = selectedOptions.ContextBudget
		s.opts.HistoryCompaction = selectedOptions.HistoryCompaction
		s.contextBudgetConfig, s.historyCompactionConfig = cfg.ContextBudget, cfg.HistoryCompaction
		s.mu.Unlock()
		_ = s.emit(msg.ID, "provider_selected", map[string]any{"provider_id": providerID, "model": model, "effort": effort, "persisted": true, "provider_reasoning_enabled": s.providerReasoningEnabled})
	case "plugins_enable":
		var payload map[string]any
		var err error
		if id := get("id"); id != "" {
			payload, err = enableRPCPluginID(userPluginService(), id)
		} else {
			s.mu.Lock()
			workspace := s.opts.Workspace
			s.mu.Unlock()
			manifestPath, pathErr := normalizePluginManifestPath(get("manifest_path"), workspace)
			if pathErr != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": pathErr.Error(), "recoverable": true})
				return
			}
			payload, err = enableRPCPlugin(userPluginService(), manifestPath)
		}
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "plugins_updated", payload)
	case "plugins_discover":
		source := get("source")
		s.startSkillOperation(msg.ID, "plugin source", "plugin_discover_started", "plugin_candidates", map[string]any{"source": source}, func(ctx context.Context) (any, error) {
			return discoverRPCPluginSource(ctx, source)
		})
	case "plugins_install":
		source, manifestPath, revision := get("source"), get("manifest_path"), get("revision")
		s.startSkillOperation(msg.ID, "plugin install", "plugin_install_started", "plugins_updated", map[string]any{"source": source, "manifest_path": manifestPath}, func(ctx context.Context) (any, error) {
			return installRPCPluginSource(ctx, userPluginService(), source, manifestPath, revision)
		})
	case "plugins_disable":
		id := get("id")
		payload, err := disableRPCPlugin(userPluginService(), id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "plugins_updated", payload)
	case "plugins_remove":
		payload, err := removeRPCPluginID(userPluginService(), get("id"))
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "plugins_updated", payload)
	case "mcp_list":
		payload, err := s.mcpCatalog(s.ctx)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "mcp_catalog", payload)
	case "mcp_add":
		var request struct {
			Server         mcpclient.ServerConfig `json:"server"`
			CredentialKind string                 `json:"credential_kind,omitempty"`
			Secret         string                 `json:"secret,omitempty"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil || request.Server.ID == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid MCP server configuration", "recoverable": true})
			return
		}
		var payload mcpCatalogPayload
		var err error
		if request.CredentialKind != "" || request.Secret != "" {
			payload, err = s.mcpAddSecret(s.ctx, request.Server, request.CredentialKind, request.Secret)
		} else {
			payload, err = s.mcpAdd(s.ctx, request.Server)
		}
		if err != nil {
			message := err.Error()
			if request.Secret != "" || request.CredentialKind != "" {
				message = "could not configure MCP server credential; check endpoint and credential fields"
			}
			_ = s.emit(msg.ID, "error", map[string]any{"message": message, "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "mcp_updated", payload)
	case "mcp_remove":
		id := get("id")
		if id == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "server id is required", "recoverable": true})
			return
		}
		payload, err := s.mcpRemove(s.ctx, id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "mcp_updated", payload)
	case "mcp_login":
		s.startMCPOAuthLogin(msg.ID, get("id"))
	case "mcp_logout":
		payload, err := s.mcpOAuthLogout(s.ctx, get("id"))
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "mcp_auth_status", payload)
	case "web_status":
		payload, err := rpcWebStatusPayload()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "could not inspect TinyFish configuration", "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "web_status", payload)
	case "web_configure":
		if err := s.webMutationAllowed(); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		var request struct {
			Secret string `json:"secret"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil || request.Secret == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid TinyFish credential request", "recoverable": true})
			return
		}
		if err := (websearch.SecretStore{Home: pkHome()}).Save(request.Secret); err != nil {
			// Do not return validation/store errors here: they must never echo
			// request data supplied alongside the secret.
			_ = s.emit(msg.ID, "error", map[string]any{"message": "could not save TinyFish credential; check the key and pk credential store", "recoverable": true})
			return
		}
		payload, err := rpcWebStatusPayload()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "TinyFish credential was saved, but its status could not be read", "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "web_status", payload)
	case "web_clear":
		if err := s.webMutationAllowed(); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		if err := (websearch.SecretStore{Home: pkHome()}).Clear(); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "could not clear TinyFish credential", "recoverable": true})
			return
		}
		payload, err := rpcWebStatusPayload()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "TinyFish credential was cleared, but its status could not be read", "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "web_status", payload)
	case "tools":
		payload, err := s.modelToolCatalog(s.ctx)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "tool_catalog", payload)
	case "skills":
		catalog, err := s.skillCatalog(s.ctx)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "skill_catalog", map[string]any{"skills": catalog.Skills, "warnings": catalog.Warnings, "saved": catalog.Saved})
	case "skill_search":
		query := get("query")
		manager := s.installManager()
		s.startSkillOperation(msg.ID, "skill", "skill_search_started", "skill_search_results", map[string]any{"query": query}, func(ctx context.Context) (any, error) {
			results, err := manager.Search(ctx, query)
			if err != nil {
				return nil, err
			}
			return map[string]any{"query": query, "results": results}, nil
		})
	case "skill_source_list":
		source := get("source")
		manager := s.installManager()
		s.startSkillOperation(msg.ID, "skill", "skill_source_list_started", "skill_source_candidates", map[string]any{"source": source}, func(ctx context.Context) (any, error) {
			candidates, err := manager.Discover(ctx, source)
			if err != nil {
				return nil, err
			}
			return map[string]any{"source": source, "candidates": candidates}, nil
		})
	case "skill_install":
		source, skillPath := get("source"), get("path")
		manager := s.installManager()
		s.startSkillOperation(msg.ID, "skill", "skill_install_started", "skill_installed", map[string]any{"source": source, "path": skillPath}, func(ctx context.Context) (any, error) {
			installed, err := manager.Install(ctx, source, skillPath)
			if err != nil {
				return nil, err
			}
			return map[string]any{"skill": installed, "next_session_only": true}, nil
		})
	case "skill_installed_list":
		installed, err := s.installManager().List()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "skill_installations", map[string]any{"skills": installed})
	case "skill_remove":
		if err := s.skillMutationAllowed(); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		name := get("name")
		if err := s.installManager().Remove(name); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "skill_removed", map[string]any{"name": name, "next_session_only": true})
	case "skill_cancel":
		s.mu.Lock()
		cancel, active := s.skillOperationCancel, s.skillOperationActive && s.skillOperationKind == "skill"
		s.mu.Unlock()
		if !active || cancel == nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "no skill network operation is running", "recoverable": true})
			return
		}
		cancel()
		_ = s.emit(msg.ID, "skill_cancel_requested", map[string]any{})
	case "session_operation_cancel":
		s.cancelRPCOperation(msg.ID, "session ", "session_operation_cancel_requested")
	case "plugin_source_cancel":
		s.cancelRPCOperation(msg.ID, "plugin ", "plugin_source_cancel_requested")
	case "skill_read":
		document, err := s.readSkill(s.ctx, get("name"))
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "skill_document", map[string]any{"skill": document.Skill, "content": document.Content})
	case "sessions_list":
		var request struct {
			Query     string `json:"query,omitempty"`
			Workspace string `json:"workspace,omitempty"`
			Limit     int    `json:"limit,omitempty"`
		}
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &request); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid session search request", "recoverable": true})
				return
			}
		}
		s.startSkillOperation(msg.ID, "session search", "sessions_list_started", "sessions", map[string]any{}, func(ctx context.Context) (any, error) {
			items, err := s.listSessions(ctx, request.Query, request.Workspace, request.Limit)
			return map[string]any{"sessions": items}, err
		})
	case "sessions_archive":
		var request struct {
			SessionIDs []string `json:"session_ids"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid session archive request", "recoverable": true})
			return
		}
		s.startSkillOperation(msg.ID, "session archive", "sessions_archive_started", "sessions_archived", map[string]any{"count": len(request.SessionIDs)}, func(ctx context.Context) (any, error) {
			return map[string]any{"results": s.archiveSessions(ctx, request.SessionIDs)}, nil
		})
	case "sessions_trash_list":
		var request struct {
			Query string `json:"query,omitempty"`
		}
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &request); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid archived session search request", "recoverable": true})
				return
			}
		}
		s.startSkillOperation(msg.ID, "session trash search", "sessions_trash_list_started", "sessions_trash", map[string]any{}, func(ctx context.Context) (any, error) {
			items, err := s.listSessionTrash(ctx, request.Query)
			return map[string]any{"sessions": items}, err
		})
	case "sessions_restore":
		var request struct {
			TrashIDs []string `json:"trash_ids"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid session restore request", "recoverable": true})
			return
		}
		s.startSkillOperation(msg.ID, "session restore", "sessions_restore_started", "sessions_restored", map[string]any{"count": len(request.TrashIDs)}, func(ctx context.Context) (any, error) {
			return map[string]any{"results": s.restoreSessions(ctx, request.TrashIDs)}, nil
		})
	case "sessions_purge":
		var request struct {
			TrashIDs []string `json:"trash_ids"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid permanent session deletion request", "recoverable": true})
			return
		}
		s.startSkillOperation(msg.ID, "session purge", "sessions_purge_started", "sessions_purged", map[string]any{"count": len(request.TrashIDs)}, func(ctx context.Context) (any, error) {
			return map[string]any{"results": s.purgeSessions(ctx, request.TrashIDs)}, nil
		})
	case "tasks":
		store, err := localfile.New(s.sessionDir)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		items, err := store.ListSessions(s.ctx)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "tasks", map[string]any{"sessions": items})
	case "task_create":
		s.mu.Lock()
		started, busy := s.started, s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.attachedTask != ""
		s.mu.Unlock()
		if !started {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before creating a task", "recoverable": true})
			return
		}
		if busy {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cannot create a task while another foreground operation is active", "recoverable": true})
			return
		}
		prompt := get("prompt")
		if strings.TrimSpace(prompt) == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "task prompt is empty", "recoverable": true})
			return
		}
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		model, effort, providerID := get("model"), get("effort"), get("provider_id")
		s.mu.Lock()
		if providerID == "" {
			providerID = s.providerID
		}
		s.mu.Unlock()
		if model == "" {
			model = cfg.Model
		}
		if effort == "" {
			effort = cfg.Effort
		}
		if !config.ValidEffort(effort) {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "unsupported reasoning effort " + effort + " (none is not supported by the current adapter)", "recoverable": true})
			return
		}
		effort = strings.ToLower(effort)
		if !config.ValidEffort(effort) {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "unsupported reasoning effort " + effort + " (none is not supported by the current adapter)", "recoverable": true})
			return
		}
		effort = strings.ToLower(effort)
		workspace := get("workspace")
		if workspace == "" {
			workspace = filepath.Join(s.opts.Workspace, "pk-work", fmt.Sprintf("task-%d", time.Now().UnixNano()))
		}
		executable, _ := os.Executable()
		budgetProviderBaseURL := ""
		if providerID != "" {
			provider, providerErr := resolveRPCProvider(providerID)
			if providerErr != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": providerErr.Error(), "recoverable": true})
				return
			}
			budgetProviderBaseURL = provider.BaseURL
		}
		taskOptions := runner.Options{ProviderID: providerID, Model: model, Effort: effort}
		if err := applyConfiguredContextManagement(&taskOptions, cfg, budgetProviderBaseURL); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "resolve task context budget: " + err.Error(), "recoverable": true})
			return
		}
		t, err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).Start(s.ctx, tasks.StartOptions{Prompt: prompt, Workspace: workspace, Model: model, Effort: effort, ContextPolicy: cfg.ContextPolicy, ProviderID: providerID, ContextBudget: taskOptions.ContextBudget, HistoryCompaction: taskOptions.HistoryCompaction, ContextBudgetConfig: cfg.ContextBudget, HistoryCompactionConfig: cfg.HistoryCompaction, SessionDir: s.sessionDir, SkillsDirs: defaultSkillDirs(), ToolEvents: true, Executable: executable})
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "task_created", taskPayload(t))
	case "task_list":
		items, err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).List()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		list := make([]map[string]any, 0, len(items))
		for _, item := range items {
			list = append(list, taskPayload(item))
		}
		_ = s.emit(msg.ID, "task_list", map[string]any{"tasks": list})
	case "task_attach":
		id := get("task_id")
		if id == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "task_id is required", "recoverable": true})
			return
		}
		s.mu.Lock()
		blocked := s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.reloadPrepared
		previousCancel := s.taskFollowCancel
		s.mu.Unlock()
		if blocked {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cannot attach a task while another foreground operation is active", "recoverable": true})
			return
		}
		store := tasks.Store{Root: filepath.Join(pkHome(), "tasks")}
		task, err := store.Get(id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		followCtx, cancel := context.WithCancel(s.ctx)
		s.mu.Lock()
		if s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.reloadPrepared {
			s.mu.Unlock()
			cancel()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cannot attach a task while another foreground operation is active", "recoverable": true})
			return
		}
		previousCancel = s.taskFollowCancel
		s.taskFollowCancel = cancel
		s.attachedTask = id
		s.taskFollowRequest = msg.ID
		s.mu.Unlock()
		if previousCancel != nil {
			previousCancel()
		}
		pendingQuestions, questionsErr := store.ListQuestions(id)
		if questionsErr != nil {
			cancel()
			_ = s.emit(msg.ID, "error", map[string]any{"message": questionsErr.Error(), "recoverable": true})
			return
		}
		attachedPayload := taskPayload(task)
		attachedPayload["pending_questions"] = taskQuestionsPayload(id, pendingQuestions)
		questionEventCursor := task.LastEvent
		_ = s.emit(msg.ID, "task_attached", attachedPayload)
		go func() {
			writer := taskOutputWriter{server: s, requestID: msg.ID, taskID: id}
			_, followErr := store.FollowEvents(followCtx, id, 0, writer, func(event tasks.Event) {
				if event.Seq <= questionEventCursor {
					return // Older pending questions are already included in task_attached.
				}
				switch event.Type {
				case "task_question":
					_ = s.emit(msg.ID, "task_question", map[string]any{
						"task_id": id, "question_id": event.QuestionID, "session_id": event.SessionID,
						"text": event.QuestionText, "choices": event.QuestionChoices, "kind": event.QuestionKind, "status": "pending",
					})
				case "task_question_answered":
					_ = s.emit(msg.ID, "task_question_answered", map[string]any{"task_id": id, "question_id": event.QuestionID})
				case "task_question_cancelled":
					_ = s.emit(msg.ID, "task_question_cancelled", map[string]any{"task_id": id, "question_id": event.QuestionID})
				}
			})
			defer func() {
				s.mu.Lock()
				if s.taskFollowRequest == msg.ID {
					s.taskFollowCancel = nil
					s.taskFollowRequest = ""
					s.attachedTask = ""
				}
				s.mu.Unlock()
			}()
			if errors.Is(followErr, context.Canceled) {
				_ = s.emit(msg.ID, "task_detached", map[string]any{"task_id": id})
				return
			}
			latest, getErr := store.Get(id)
			if getErr != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": getErr.Error(), "recoverable": true})
				return
			}
			payload := map[string]any{"task_id": id, "status": latest.Status, "error": latest.Error, "session_id": latest.SessionID}
			if followErr != nil {
				payload["error"] = followErr.Error()
			}
			_ = s.emit(msg.ID, "task_finished", payload)
		}()
	case "task_status":
		id := get("task_id")
		store := tasks.Store{Root: filepath.Join(pkHome(), "tasks")}
		task, err := store.Get(id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		questions, err := store.ListQuestions(id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		payload := taskPayload(task)
		payload["pending_questions"] = taskQuestionsPayload(id, questions)
		_ = s.emit(msg.ID, "task_status", payload)
	case "task_question_answer":
		taskID, questionID, answer := get("task_id"), get("question_id"), get("answer")
		if err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).AnswerQuestion(taskID, questionID, answer); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "task_question_answered", map[string]any{"task_id": taskID, "question_id": questionID})
	case "task_question_cancel":
		taskID, questionID := get("task_id"), get("question_id")
		if err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).CancelQuestion(taskID, questionID); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "task_question_cancelled", map[string]any{"task_id": taskID, "question_id": questionID})
	case "task_cancel":
		id := get("task_id")
		if err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).Cancel(id); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "task_cancelled", map[string]any{"task_id": id})
	case "task_resume":
		id := get("task_id")
		task, err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).Resume(s.ctx, id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "task_resumed", taskPayload(task))
	case "send_input":
		id, text := get("task_id"), get("text")
		if err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).SendInput(id, text); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "task_input_sent", map[string]any{"task_id": id})
	case "status":
		status := auth.Status()
		s.mu.Lock()
		sessionID, model, effort, providerID := s.session, s.opts.Model, s.opts.Effort, s.providerID
		s.mu.Unlock()
		_ = s.emit(msg.ID, "status", map[string]any{"session_id": sessionID, "model": model, "effort": effort, "provider_id": providerID, "logged_in": status.LoggedIn, "expired": status.Expired, "account_id": status.AccountID})
	case "login":
		if err := auth.Login(s.ctx, auth.LoginOptions{Output: s.diagnostics}); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "status", map[string]any{"logged_in": true})
	case "detach":
		s.mu.Lock()
		active, sessionID := s.active, s.session
		if active {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cancel the active turn before detaching", "recoverable": true})
			return
		}
		followCancel := s.taskFollowCancel
		s.taskFollowCancel = nil
		s.taskFollowRequest = ""
		s.attachedTask = ""
		s.mu.Unlock()
		if followCancel != nil {
			followCancel()
		}
		_ = s.emit(msg.ID, "detached", map[string]any{"session_id": sessionID})
	case "set_model":
		requestedModel, requestedEffort := strings.TrimSpace(get("model")), strings.TrimSpace(get("effort"))
		s.mu.Lock()
		busy := s.active || s.skillOperationActive || s.pluginCommandActive || s.releaseActive || s.attachedTask != ""
		model, effort, sessionID, providerID := s.opts.Model, s.opts.Effort, s.session, s.providerID
		s.mu.Unlock()
		if busy {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "model settings can only be changed while the session is idle", "recoverable": true})
			return
		}
		if requestedModel != "" {
			if len(requestedModel) > 256 || strings.ContainsAny(requestedModel, "\r\n\x00") {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "model ID must be at most 256 bytes and contain no control characters", "recoverable": true})
				return
			}
			model = requestedModel
		}
		if requestedEffort != "" {
			if !config.ValidEffort(requestedEffort) {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "unsupported reasoning effort " + requestedEffort, "recoverable": true})
				return
			}
			effort = strings.ToLower(requestedEffort)
		}
		if providerID == "" && isCloudflareWorkersAIModel(model) {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "a Workers AI model cannot be configured for Native Codex; select Cloudflare Workers AI first", "recoverable": true})
			return
		}
		if model != "" && effort != "" {
			cfg, err := config.Load(s.cfgPath)
			if err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "could not load pk defaults: " + err.Error(), "recoverable": true})
				return
			}
			providerBaseURL := rpcContextBudgetBaseURL(providerID)
			selectedOptions := runner.Options{ProviderID: providerID, Model: model, Effort: effort}
			if err := applyConfiguredContextManagement(&selectedOptions, cfg, providerBaseURL); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "resolve context budget: " + err.Error(), "recoverable": true})
				return
			}
			if providerID == "" {
				cfg.Model, cfg.Effort = model, effort
				if err := config.Save(s.cfgPath, cfg); err != nil {
					_ = s.emit(msg.ID, "error", map[string]any{"message": "could not save model defaults: " + err.Error(), "recoverable": true})
					return
				}
			} else {
				provider, err := resolveRPCProvider(providerID)
				if err != nil {
					_ = s.emit(msg.ID, "error", map[string]any{"message": "load selected provider: " + err.Error(), "recoverable": true})
					return
				}
				var effortPreference *string
				if requestedEffort != "" && provider.SupportsReasoningEffort {
					effortPreference = &effort
				}
				if err := rpcProviderStore().SetDefaultModelAndProvider(providerID, model, effortPreference); err != nil {
					_ = s.emit(msg.ID, "error", map[string]any{"message": "could not save provider model preference: " + err.Error(), "recoverable": true})
					return
				}
			}
			s.mu.Lock()
			s.opts.Model, s.opts.Effort = model, effort
			s.opts.ContextBudget, s.opts.HistoryCompaction = selectedOptions.ContextBudget, selectedOptions.HistoryCompaction
			s.contextBudgetConfig, s.historyCompactionConfig = cfg.ContextBudget, cfg.HistoryCompaction
			s.mu.Unlock()
		}
		_ = s.emit(msg.ID, "status", map[string]any{"session_id": sessionID, "model": model, "effort": effort, "provider_id": providerID})
	case "shutdown":
		s.quit = true
		_ = s.emit(msg.ID, "shutdown", map[string]any{"session_id": s.session})
	default:
		_ = s.emit(msg.ID, "error", map[string]any{"message": "unknown command type " + msg.Type, "recoverable": true})
	}
}

func taskQuestionsPayload(taskID string, questions []tasks.Question) []map[string]any {
	payload := make([]map[string]any, 0, len(questions))
	for _, question := range questions {
		payload = append(payload, map[string]any{
			"task_id": taskID, "question_id": question.ID, "session_id": question.SessionID,
			"text": question.Text, "choices": question.Choices, "kind": question.Kind, "status": "pending",
		})
	}
	return payload
}

// workspaceAttachmentPaths converts absolute paths inside the selected workspace
// to the loader's workspace-relative form. Relative paths are already interpreted
// from that workspace, never from the process's unrelated launch directory.
func workspaceAttachmentPaths(workspace string, paths []string) ([]string, error) {
	resolved := make([]string, 0, len(paths))
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return nil, errors.New("attachment path must not be empty")
		}
		if filepath.IsAbs(path) {
			rel, err := filepath.Rel(root, filepath.Clean(path))
			if err != nil {
				return nil, fmt.Errorf("resolve attachment %q: %w", path, err)
			}
			path = rel
		}
		resolved = append(resolved, path)
	}
	return resolved, nil
}

func taskPayload(t tasks.Task) map[string]any {
	return map[string]any{"task_id": t.ID, "status": t.Status, "prompt": t.Prompt, "workspace": t.Workspace, "model": t.Model, "effort": t.Effort, "updated_at": t.UpdatedAt, "last_event": t.LastEvent, "session_id": t.SessionID, "error": t.Error}
}

type historyEntry struct {
	Role        string                    `json:"role"`
	Text        string                    `json:"text"`
	Sequence    uint64                    `json:"sequence"`
	Name        string                    `json:"name,omitempty"`
	State       string                    `json:"state,omitempty"`
	ToolCallID  string                    `json:"tool_call_id,omitempty"`
	Attachments []presentation.Attachment `json:"attachments,omitempty"`
}

const (
	maxHistoryEntries    = 100
	maxHistoryBytes      = 64 << 10
	maxHistoryEntryBytes = 8 << 10
)

func (s *rpcServer) emitSessionHistory(requestID, id string) error {
	store, err := localfile.New(s.sessionDir)
	if err != nil {
		return err
	}
	entries, hasEarlier, truncated, beforeSequence, err := sessionHistoryPage(s.ctx, store, id, 0, s.sessionDir)
	if err != nil {
		return err
	}
	return s.emit(requestID, "history", map[string]any{"session_id": id, "entries": entries, "truncated": truncated, "has_earlier": hasEarlier, "before_sequence": beforeSequence})
}

func recentSessionHistory(ctx context.Context, store *localfile.Store, id string) ([]historyEntry, bool, error) {
	entries, hasEarlier, truncated, _, err := sessionHistoryPage(ctx, store, id, 0)
	return entries, truncated || hasEarlier, err
}

// sessionHistoryPage returns the bounded page immediately before beforeSequence.
// A zero cursor means the newest page. Items() loads the backing event log, so
// each request performs one store read and returns only the small transcript
// projection; UI paging never accumulates the full transcript in memory.
func sessionHistoryPage(ctx context.Context, store *localfile.Store, id string, beforeSequence uint64, presentationDirs ...string) ([]historyEntry, bool, bool, uint64, error) {
	maxInt := uint64(^uint(0) >> 1)
	limit := int(maxInt)
	if beforeSequence > 0 {
		if beforeSequence > maxInt {
			return nil, false, false, 0, fmt.Errorf("history cursor is out of range")
		}
		if beforeSequence == 1 {
			return []historyEntry{}, false, false, 0, nil
		}
		limit = int(beforeSequence - 1)
	}
	page, err := store.Items(ctx, session.ID(id), 0, limit)
	if err != nil {
		return nil, false, false, 0, err
	}
	entries, hasEarlier, truncated := projectHistory(page.Items)
	if len(presentationDirs) > 0 && presentationDirs[0] != "" {
		var presentationEarlier, presentationTruncated bool
		entries, presentationEarlier, presentationTruncated = enrichHistoryPresentation(presentationDirs[0], id, page.Items, entries)
		hasEarlier = hasEarlier || presentationEarlier
		truncated = truncated || presentationTruncated
	}
	var cursor uint64
	if len(entries) > 0 {
		cursor = entries[0].Sequence
	}
	return entries, hasEarlier, truncated, cursor, nil
}

func projectHistory(items []sessionstore.Item) ([]historyEntry, bool, bool) {
	type toolKey struct {
		turn session.TurnID
		call string
	}
	type callInfo struct{ name, arguments string }
	type callHistory struct {
		info     callInfo
		sequence uint64
		state    string
		detail   string
	}
	calls := map[toolKey]callInfo{}
	toolRows := map[toolKey]*callHistory{}
	// First collect call metadata and latest persisted state. A row is anchored
	// at the first status event, whose sequence is unique even when one model
	// response contains several parallel tool calls.
	for _, item := range items {
		switch item.Kind {
		case sessionstore.ItemModelResponse:
			response, ok := item.Data.(sessionstore.ModelResponse)
			if !ok {
				continue
			}
			for _, output := range response.Response.Output {
				if output.Type == llm.ItemToolCall {
					if call, ok := output.Data.(llm.ToolCall); ok {
						calls[toolKey{response.TurnID, call.CallID}] = callInfo{name: call.Name, arguments: call.Arguments}
					}
				}
			}
		case sessionstore.ItemToolCallStatus:
			status, ok := item.Data.(sessionstore.ToolCallStatus)
			if !ok {
				continue
			}
			key := toolKey{status.TurnID, status.CallID}
			row := toolRows[key]
			if row == nil {
				row = &callHistory{sequence: uint64(item.Sequence), state: "running"}
				toolRows[key] = row
			}
			row.info = calls[key]
			row.state, row.detail = historyToolState(status.Status, status.Operations)
		}
	}
	entries := make([]historyEntry, 0, maxHistoryEntries)
	hasEarlier, truncated := false, false
	readBytes := 0
	for _, item := range items {
		var role, text string
		var extra historyEntry
		switch item.Kind {
		case sessionstore.ItemInput:
			if input, ok := item.Data.(inbox.Input); ok && input.Kind == inbox.InputExternal {
				var value string
				if json.Unmarshal([]byte(input.Payload), &value) == nil {
					role, text = "user", value
				}
			}
		case sessionstore.ItemModelResponse:
			if response, ok := item.Data.(sessionstore.ModelResponse); ok {
				var previewTruncated bool
				text, previewTruncated = assistantHistoryPreview(response.Response.Output, maxHistoryEntryBytes)
				if text != "" {
					role = "assistant"
					truncated = truncated || previewTruncated
				}
			}
		case sessionstore.ItemToolCallStatus:
			status, ok := item.Data.(sessionstore.ToolCallStatus)
			if ok {
				key := toolKey{status.TurnID, status.CallID}
				if row := toolRows[key]; row != nil && row.sequence == uint64(item.Sequence) {
					role, text = "tool", historyToolText(row.info.arguments, row.detail)
					extra = historyEntry{Name: historyPrefixUTF8(row.info.name, 256), State: row.state, ToolCallID: historyPrefixUTF8(status.CallID, 256)}
				}
			}
		}
		if text == "" {
			continue
		}
		extra.Role, extra.Text, extra.Sequence = role, text, uint64(item.Sequence)
		entryLimit := maxHistoryEntryBytes - (historyEntrySize(extra) - len(text))
		if entryLimit < 0 {
			entryLimit = 0
		}
		if len(text) > entryLimit {
			text = tailUTF8(text, entryLimit)
			extra.Text = text
			truncated = true
		}
		for len(entries) > 0 && (len(entries) >= maxHistoryEntries || readBytes+historyEntrySize(extra) > maxHistoryBytes) {
			readBytes -= historyEntrySize(entries[0])
			entries = entries[1:]
			hasEarlier = true
			truncated = true
		}
		if historyEntrySize(extra) > maxHistoryBytes {
			text = tailUTF8(text, maxHistoryBytes-(historyEntrySize(extra)-len(text)))
			extra.Text = text
			truncated = true
		}
		if text != "" {
			extra.Text = text
			entries = append(entries, extra)
			readBytes += historyEntrySize(extra)
		}
	}
	return entries, hasEarlier, truncated
}

func historyEntrySize(entry historyEntry) int {
	size := len(entry.Text) + len(entry.Name) + len(entry.State) + len(entry.ToolCallID)
	for _, attachment := range entry.Attachments {
		size += len(attachment.Name) + len(attachment.Kind) + len(attachment.ContentType) + 24
	}
	return size
}

func historyToolState(status tool.CallStatus, operations []operation.Operation) (string, string) {
	if status.Error != "" {
		return "failed", status.Error
	}
	if len(status.WaitingFor) == 0 {
		return "completed", ""
	}
	byID := make(map[operation.ID]operation.Operation, len(operations))
	for _, op := range operations {
		byID[op.ID] = op
	}
	allTerminal := true
	canceled := false
	var details []string
	for _, id := range status.WaitingFor {
		op, ok := byID[id]
		if !ok {
			allTerminal = false
			continue
		}
		switch op.Status {
		case operation.StatusFailed:
			return "failed", historyToolOperationDetails(operations)
		case operation.StatusCanceled:
			canceled = true
		case operation.StatusCompleted:
		default:
			allTerminal = false
		}
		if op.Type == operation.TypeRemoteJob {
			if decoded, err := operation.DecodeRemoteJobState(op); err == nil {
				if decoded.TerminalError != "" {
					details = append(details, decoded.TerminalError)
				} else if decoded.TerminalResult != "" {
					details = append(details, decoded.TerminalResult)
				}
			}
		} else if op.Type == operation.TypeShell {
			if decoded, err := operation.DecodeShellState(op); err == nil {
				if len(decoded.InlineOut) > 0 {
					details = append(details, string(decoded.InlineOut))
				}
				if len(decoded.InlineErr) > 0 {
					details = append(details, string(decoded.InlineErr))
				}
				if decoded.TerminalError != "" {
					details = append(details, decoded.TerminalError)
				}
			}
		}
	}
	if !allTerminal {
		return "running", strings.Join(details, "\n")
	}
	if canceled {
		return "canceled", strings.Join(details, "\n")
	}
	return "completed", strings.Join(details, "\n")
}

func historyToolOperationDetails(operations []operation.Operation) string {
	var details []string
	for _, op := range operations {
		if op.Type == operation.TypeRemoteJob {
			if decoded, err := operation.DecodeRemoteJobState(op); err == nil {
				if decoded.TerminalError != "" {
					details = append(details, decoded.TerminalError)
				} else if decoded.TerminalResult != "" {
					details = append(details, decoded.TerminalResult)
				}
			}
		} else if op.Type == operation.TypeShell {
			if decoded, err := operation.DecodeShellState(op); err == nil {
				if len(decoded.InlineOut) > 0 {
					details = append(details, string(decoded.InlineOut))
				}
				if len(decoded.InlineErr) > 0 {
					details = append(details, string(decoded.InlineErr))
				}
				if decoded.TerminalError != "" {
					details = append(details, decoded.TerminalError)
				}
			}
		}
	}
	return strings.Join(details, "\n")
}

func historyToolText(arguments, result string) string {
	var parts []string
	if arguments != "" {
		parts = append(parts, "Arguments: "+historyPrefixUTF8(redactHistoryJSON(arguments), 3<<10))
	}
	if result != "" {
		parts = append(parts, "Result: "+historyPrefixUTF8(redactHistoryText(result), 3<<10))
	}
	if len(parts) == 0 {
		return "No arguments or result recorded."
	}
	return strings.Join(parts, "\n")
}

func historyPrefixUTF8(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes - len("…")
	for cut > 0 && cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func redactHistoryJSON(raw string) string {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return redactHistoryText(raw)
	}
	var redact func(any, string) any
	redact = func(v any, key string) any {
		key = strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(key))
		for _, part := range []string{"token", "password", "secret", "credential", "authorization", "api_key", "apikey", "private_key", "privatekey"} {
			if strings.Contains(key, part) {
				return "[redacted]"
			}
		}
		switch x := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, item := range x {
				out[k] = redact(item, k)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = redact(item, key)
			}
			return out
		case string:
			return redactHistoryText(x)
		default:
			return v
		}
	}
	encoded, err := json.Marshal(redact(value, ""))
	if err != nil {
		return "[unavailable]"
	}
	return string(encoded)
}

func redactHistoryText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	return historyBearerPattern.ReplaceAllString(value, "Bearer [redacted]")
}

var historyBearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)

func tailUTF8(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

type taskOutputWriter struct {
	server            *rpcServer
	requestID, taskID string
}

func (w taskOutputWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.server.emit(w.requestID, "task_output", map[string]any{"task_id": w.taskID, "text": string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

type rpcRunnerOutput struct {
	server *rpcServer
	id     string
	mu     sync.Mutex
	buffer []byte
}

func (w *rpcRunnerOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer = append(w.buffer, data...)
	for {
		i := 0
		for i < len(w.buffer) && w.buffer[i] != '\n' {
			i++
		}
		if i == len(w.buffer) {
			break
		}
		line := append([]byte(nil), w.buffer[:i]...)
		w.buffer = w.buffer[i+1:]
		var obj map[string]any
		if json.Unmarshal(line, &obj) == nil {
			typ, _ := obj["type"].(string)
			delete(obj, "type")
			delete(obj, "session_id")
			_ = w.server.emit(w.id, typ, obj)
		}
	}
	return len(data), nil
}

func locateUIEntry(exe string) (string, error) {
	abs, _ := filepath.Abs(exe)
	dir := filepath.Dir(abs)
	candidates := []string{filepath.Join(dir, "..", "lib", "pk", "ui", "main.js"), filepath.Join(dir, "..", "lib", "pk", "ui", "dist", "main.js"), filepath.Join(dir, "ui", "dist", "main.js"), filepath.Join(dir, "ui", "src", "main.tsx"), filepath.Join(dir, "..", "ui", "dist", "main.js"), filepath.Join(dir, "..", "ui", "src", "main.tsx")}
	if v := os.Getenv("PK_UI_ENTRY"); v != "" {
		candidates = append([]string{v}, candidates...)
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
			return candidate, nil
		}
	}
	if _, err := exec.LookPath("bun"); err != nil {
		return "", errors.New("Bun is required for the OpenTUI frontend; install Bun or use --plain")
	}
	return "", fmt.Errorf("OpenTUI frontend not found next to pk; rebuild/install the ui assets or use --plain")
}
