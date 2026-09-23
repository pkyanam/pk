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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/clipboard"
	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/extensions"
	"github.com/pkyanam/pk/internal/interaction"
	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/modelstream"
	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/subagents"
	"github.com/pkyanam/pk/internal/tasks"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

const rpcVersion = 1

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
	ctx                 context.Context
	input               io.Reader
	output, diagnostics io.Writer
	cfgPath, sessionDir string
	mu                  sync.Mutex
	opts                runner.Options
	adapter             *codexAdapter
	useCodex            bool
	codexPath           string
	started             bool
	steeringEnabled     bool
	session             string
	providerID          string
	activeCancel        context.CancelFunc
	releaseCancel       context.CancelFunc
	releaseDone         chan struct{}
	releaseActive       bool
	pluginCommandCancel context.CancelFunc
	pluginCommandDone   chan struct{}
	pluginCommandActive bool
	reloadPrepared      bool
	activeInputs        chan runner.Input
	pendingSteers       map[string]func(error)
	active              bool
	quit                bool
	attachedTask        string
	taskFollowRequest   string
	taskFollowCancel    context.CancelFunc
	pluginPaths         []string
	pluginIssues        []string
	requestTypes        map[string]string
	broker              *interaction.Broker
	loadAttachments     func(context.Context, string, string, []string) (string, []attachments.Attachment, error)
	prepareAdapter      func(context.Context, bool, string) (*codexAdapter, error)
	clipboardProvider   clipboard.Provider
	clipboardWriter     clipboard.TextWriter
	runReleaseCommand   func(context.Context, []string, io.Writer, io.Writer) int
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

func (s *rpcServer) rejectSteer(requestID, message string) {
	s.mu.Lock()
	sessionID := s.session
	s.mu.Unlock()
	_ = s.emit(requestID, "input_rejected", map[string]any{"input_id": requestID, "session_id": sessionID, "message": message})
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
	if s.active || s.releaseActive || s.pluginCommandActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared {
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
			code = runUpdateWithProgress(ctx, args, writer, writer, func(stage string) {
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
		"source_validate":  "Validating the selected pk source.",
		"clone":            "Cloning the official pk source into an isolated checkout.",
		"resolve_revision": "Resolving the requested pk revision.",
		"copy":             "Copying source into an isolated build directory.",
		"test":             "Running Go tests.",
		"dependencies":     "Installing production UI dependencies.",
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
		requestedProviderID := get("provider_id")
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
				if requestedProviderID != "" && requestedProviderID != providerID {
					_ = s.emit(msg.ID, "error", map[string]any{"message": "the requested provider does not match this saved session; select the original provider or start a new session", "recoverable": true})
					return
				}
			} else {
				providerID, err = resolveRPCProviderID(requestedProviderID)
			}
		} else {
			providerID, err = resolveRPCProviderID(requestedProviderID)
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
		s.opts = runner.Options{Workspace: workspace, Model: model, Effort: effort, ProviderID: providerID, SessionDir: s.sessionDir, SkillsDirs: defaultSkillDirs(), ToolEvents: true}
		s.providerID = providerID
		s.session = sessionID
		s.opts.SessionID = s.session
		s.pluginPaths = pluginPaths
		s.pluginIssues = pluginIssueMessages(pluginIssues)
		var steeringEnabled bool
		_ = json.Unmarshal(payload["steering"], &steeringEnabled)
		s.steeringEnabled = steeringEnabled
		if s.session != "" {
			if err := s.emitSessionHistory(msg.ID, s.session); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
				return
			}
		}
		s.started = true
		capabilities := rpcCapabilities(steeringEnabled)
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": s.session, "model": model, "effort": effort, "provider_id": providerID, "capabilities": capabilities})
		if s.session != "" {
			_ = s.emit(msg.ID, "session", map[string]any{"session_id": s.session})
		}
	case "prompt":
		text := get("text")
		if strings.TrimSpace(text) == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "prompt text is empty", "recoverable": true})
			return
		}
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
		if s.active || s.pluginCommandActive {
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
		pluginPaths := append([]string(nil), s.pluginPaths...)
		ctx, cancel := context.WithCancel(s.ctx)
		s.activeCancel = cancel
		s.active = true
		var inputStream chan runner.Input
		steeringEnabled := s.steeringEnabled
		if steeringEnabled {
			inputStream = make(chan runner.Input, 64)
			s.activeInputs = inputStream
		} else {
			s.activeInputs = nil
		}
		broker := interaction.NewBroker(ctx, sessionID)
		s.broker = broker
		opts := s.opts
		opts.SessionID = sessionID
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
				loader := s.loadAttachments
				if loader == nil {
					loader = loadPromptAttachments
				}
				var loaded []attachments.Attachment
				text, loaded, err = loader(ctx, workspace, text, resolved)
				if err != nil {
					finished <- turnDone{id: msg.ID, err: fmt.Errorf("load attachments: %w", err)}
					return
				}
				if err := ctx.Err(); err != nil {
					finished <- turnDone{id: msg.ID, err: err}
					return
				}
				summaries := make([]map[string]any, 0, len(loaded))
				for _, item := range loaded {
					summaries = append(summaries, map[string]any{"path": item.Path, "kind": item.Kind, "content_type": item.ContentType, "truncated": item.Truncated, "pages_extracted": item.PagesExtracted, "pages_total": item.PagesTotal})
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
				_ = s.emit(msg.ID, "session", map[string]any{"session_id": id})
			}
			host, err := configureRPCPluginSession(broker.Context(), &opts, pluginPaths, broker, s.diagnostics, tinyFishRegistryExtension())
			if err != nil {
				finished <- turnDone{id: msg.ID, err: fmt.Errorf("load session plugins: %w", err)}
				return
			}
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
		snapshot, err := provider.Read(s.ctx)
		if err != nil {
			_ = s.emit(msg.ID, "clipboard_files", map[string]any{"files": []clipboard.SelectedFile{}, "text": "", "message": err.Error()})
			return
		}
		snapshot = clipboard.NormalizeSnapshot(snapshot)
		selected, err := clipboard.CaptureSnapshot(s.ctx, snapshot, filepath.Join(pkHome(), "attachments"))
		if err != nil {
			_ = s.emit(msg.ID, "clipboard_files", map[string]any{"files": []clipboard.SelectedFile{}, "text": snapshot.Text, "message": err.Error()})
			return
		}
		// The UI retains these explicit selections and submits their paths through
		// the same bounded attachment loader used by --file and ordinary prompts.
		_ = s.emit(msg.ID, "clipboard_files", map[string]any{"files": selected, "text": snapshot.Text, "workspace": workspace, "message": snapshot.Message})
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
		if err := writer.WriteText(s.ctx, text); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "clipboard_written", map[string]any{"bytes": len(text)})
	case "cancel":
		s.mu.Lock()
		cancel, active, sessionID, model, effort := s.activeCancel, s.active, s.session, s.opts.Model, s.opts.Effort
		s.activeInputs = nil
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
		busy := s.active || s.releaseActive || s.pluginCommandActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared
		handoff := reloadHandoff{Workspace: s.opts.Workspace, SessionID: s.session, Model: s.opts.Model, Effort: s.opts.Effort}
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
		if strings.TrimSpace(text) == "" {
			s.rejectSteer(msg.ID, "steering text is empty")
			return
		}
		if raw, present := payload["files"]; present && len(raw) > 0 {
			var files []json.RawMessage
			if err := json.Unmarshal(raw, &files); err != nil || len(files) != 0 {
				s.rejectSteer(msg.ID, "steering attachments are not supported; files were not queued")
				return
			}
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
		var replyOnce sync.Once
		reply := func(acceptedErr error) {
			replyOnce.Do(func() {
				<-queuedEvent
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
		inputStream := s.activeInputs
		sessionID := s.session
		if !s.active || inputStream == nil {
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
		steerInput := runner.Input{ID: inputID, Text: text, Accepted: reply}
		select {
		case inputStream <- steerInput:
			s.mu.Unlock()
			_ = s.emit(msg.ID, "input_queued", map[string]any{"input_id": inputID, "session_id": sessionID})
			close(queuedEvent)
		default:
			delete(s.pendingSteers, inputID)
			s.mu.Unlock()
			close(queuedEvent)
			s.rejectSteer(msg.ID, "interactive input queue is full; text was not queued")
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
		active := s.active || s.releaseActive || s.pluginCommandActive
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
		workspace, model, effort := s.opts.Workspace, s.opts.Model, s.opts.Effort
		steeringEnabled, providerID := s.steeringEnabled, s.providerID
		s.mu.Unlock()
		if err := s.emitSessionHistory(msg.ID, id); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "session", map[string]any{"session_id": id})
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": id, "model": model, "effort": effort, "provider_id": providerID, "attached": true, "capabilities": rpcCapabilities(steeringEnabled)})
	case "new":
		s.mu.Lock()
		active, sessionID := s.active || s.releaseActive || s.pluginCommandActive, s.session
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
		s.mu.Lock()
		s.session = ""
		s.opts.SessionID = ""
		s.pluginPaths = pluginPaths
		s.pluginIssues = pluginIssueMessages(pluginIssues)
		workspace, model, effort := s.opts.Workspace, s.opts.Model, s.opts.Effort
		steeringEnabled, providerID := s.steeringEnabled, s.providerID
		s.mu.Unlock()
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": "", "previous_session_id": sessionID, "model": model, "effort": effort, "provider_id": providerID, "capabilities": rpcCapabilities(steeringEnabled)})
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
		if s.active || s.releaseActive || s.pluginCommandActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared {
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
	case "providers_list":
		items, err := rpcProviderStore().Summaries()
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "providers", map[string]any{"providers": items})
	case "provider_models":
		providerID := get("provider_id")
		if providerID == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "provider_id is required", "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "provider_models_started", map[string]any{"provider_id": providerID})
		models, err := rpcProviderModels(s.ctx, providerID)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "provider_models", map[string]any{"provider_id": providerID, "models": models})
	case "provider_add":
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(msg.Payload, &raw); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid provider configuration: " + err.Error(), "recoverable": true})
			return
		}
		if _, hasLiteralKey := raw["api_key"]; hasLiteralKey {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "literal API keys cannot be sent over RPC; configure api_key_env or use `pk provider add --api-key-stdin`", "recoverable": true})
			return
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
		if err := rpcProviderStore().Put(provider); err != nil {
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
		providerID := get("provider_id")
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
		s.mu.Lock()
		if s.active || s.session != "" || s.attachedTask != "" {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "provider selection is available only before the first prompt in a new session", "recoverable": true})
			return
		}
		s.providerID, s.opts.ProviderID, s.opts.Model, s.opts.Effort = providerID, providerID, model, effort
		s.mu.Unlock()
		_ = s.emit(msg.ID, "provider_selected", map[string]any{"provider_id": providerID, "model": model, "effort": effort})
	case "plugins_enable":
		s.mu.Lock()
		workspace := s.opts.Workspace
		s.mu.Unlock()
		manifestPath, err := normalizePluginManifestPath(get("manifest_path"), workspace)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		payload, err := enableRPCPlugin(userPluginService(), manifestPath)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "plugins_updated", payload)
	case "plugins_disable":
		payload, err := disableRPCPlugin(userPluginService(), get("id"))
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
			Server mcpclient.ServerConfig `json:"server"`
		}
		if err := json.Unmarshal(msg.Payload, &request); err != nil || request.Server.ID == "" {
			if err == nil {
				err = errors.New("server.id is required")
			}
			_ = s.emit(msg.ID, "error", map[string]any{"message": "invalid MCP server configuration: " + err.Error(), "recoverable": true})
			return
		}
		payload, err := s.mcpAdd(s.ctx, request.Server)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
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
	case "skill_read":
		document, err := s.readSkill(s.ctx, get("name"))
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "skill_document", map[string]any{"skill": document.Skill, "content": document.Content})
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
		started, busy := s.started, s.active || s.releaseActive || s.pluginCommandActive || s.attachedTask != ""
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
		cfg, _ := config.Load(s.cfgPath)
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
		t, err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).Start(s.ctx, tasks.StartOptions{Prompt: prompt, Workspace: workspace, Model: model, Effort: effort, ProviderID: providerID, SessionDir: s.sessionDir, SkillsDirs: []string{filepath.Join(userHome(), ".codex", "skills"), filepath.Join(userHome(), ".agents", "skills")}, ToolEvents: true, Executable: executable})
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
		blocked := s.active || s.releaseActive || s.pluginCommandActive || s.reloadPrepared
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
		if s.active || s.releaseActive || s.pluginCommandActive || s.reloadPrepared {
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
		_ = s.emit(msg.ID, "task_attached", taskPayload(task))
		go func() {
			writer := taskOutputWriter{server: s, requestID: msg.ID, taskID: id}
			_, followErr := store.Follow(followCtx, id, 0, writer)
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
		model, effort := get("model"), get("effort")
		s.mu.Lock()
		if model != "" {
			s.opts.Model = model
		}
		if effort != "" {
			s.opts.Effort = effort
		}
		model, effort, sessionID, providerID := s.opts.Model, s.opts.Effort, s.session, s.providerID
		s.mu.Unlock()
		if model != "" && effort != "" {
			if err := config.Save(s.cfgPath, config.Config{Model: model, Effort: effort}); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "could not save model defaults: " + err.Error(), "recoverable": true})
				return
			}
		}
		_ = s.emit(msg.ID, "status", map[string]any{"session_id": sessionID, "model": model, "effort": effort, "provider_id": providerID})
	case "shutdown":
		s.quit = true
		_ = s.emit(msg.ID, "shutdown", map[string]any{"session_id": s.session})
	default:
		_ = s.emit(msg.ID, "error", map[string]any{"message": "unknown command type " + msg.Type, "recoverable": true})
	}
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
	Role     string `json:"role"`
	Text     string `json:"text"`
	Sequence uint64 `json:"sequence"`
}

const (
	historyPageSize      = 100
	maxHistoryEntries    = 100
	maxHistoryBytes      = 64 << 10
	maxHistoryEntryBytes = 8 << 10
)

func (s *rpcServer) emitSessionHistory(requestID, id string) error {
	store, err := localfile.New(s.sessionDir)
	if err != nil {
		return err
	}
	entries, truncated, err := recentSessionHistory(s.ctx, store, id)
	if err != nil {
		return err
	}
	return s.emit(requestID, "history", map[string]any{"session_id": id, "entries": entries, "truncated": truncated})
}

func recentSessionHistory(ctx context.Context, store *localfile.Store, id string) ([]historyEntry, bool, error) {
	entries := make([]historyEntry, 0, maxHistoryEntries)
	var after sessionstore.Sequence
	truncated := false
	readBytes := 0
	for {
		page, err := store.Items(ctx, session.ID(id), after, historyPageSize)
		if err != nil {
			return nil, false, err
		}
		for _, item := range page.Items {
			var role, text string
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
					var parts []string
					for _, output := range response.Response.Output {
						if output.Type == llm.ItemMessage {
							if message, ok := output.Data.(llm.Message); ok && message.Role == llm.RoleAssistant && message.Phase != "analysis" && strings.TrimSpace(message.Text) != "" {
								parts = append(parts, message.Text)
							}
						}
					}
					if len(parts) > 0 {
						role, text = "assistant", strings.Join(parts, "\n")
					}
				}
			}
			if text == "" {
				continue
			}
			if len(text) > maxHistoryEntryBytes {
				text = tailUTF8(text, maxHistoryEntryBytes)
				truncated = true
			}
			for len(entries) > 0 && (len(entries) >= maxHistoryEntries || readBytes+len(text) > maxHistoryBytes) {
				readBytes -= len(entries[0].Text)
				entries = entries[1:]
				truncated = true
			}
			if len(text) > maxHistoryBytes {
				text = tailUTF8(text, maxHistoryBytes)
				truncated = true
			}
			if text != "" {
				entries = append(entries, historyEntry{Role: role, Text: text, Sequence: uint64(item.Sequence)})
				readBytes += len(text)
			}
			if readBytes >= maxHistoryBytes {
				break
			}
		}
		previousAfter := after
		after = page.NextAfter
		if !page.More {
			break
		}
		if page.NextAfter <= previousAfter {
			return nil, false, fmt.Errorf("session history cursor did not advance")
		}
	}
	return entries, truncated, nil
}

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
