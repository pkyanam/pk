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
	"github.com/pkyanam/pk/internal/interaction"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/tasks"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
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
	session             string
	activeCancel        context.CancelFunc
	active              bool
	quit                bool
	attachedTask        string
	taskFollowCancel    context.CancelFunc
	requestTypes        map[string]string
	broker              *interaction.Broker
	loadAttachments     func(context.Context, string, string, []string) (string, []attachments.Attachment, error)
	prepareAdapter      func(context.Context, bool, string) (*codexAdapter, error)
	clipboardProvider   clipboard.Provider
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
	broker := s.broker
	s.broker = nil
	sessionID := s.session
	s.mu.Unlock()
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
		model := get("model")
		if model == "" {
			model = strings.TrimSpace(os.Getenv("PK_MODEL"))
			if model == "" {
				model = cfg.Model
			}
		}
		effort := get("effort")
		if effort == "" {
			effort = strings.TrimSpace(os.Getenv("PK_EFFORT"))
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
		s.opts = runner.Options{Workspace: workspace, Model: model, Effort: effort, SessionDir: s.sessionDir, ToolEvents: true}
		s.session = get("session_id")
		s.opts.SessionID = s.session
		if s.session != "" {
			if err := s.emitSessionHistory(msg.ID, s.session); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
				return
			}
		}
		if err := ensurePrivateDirectory(pkHome()); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		s.started = true
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": s.session, "model": model, "effort": effort, "capabilities": []string{"attach", "cancel", "detach", "set_model"}})
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
		if s.active {
			s.mu.Unlock()
			_ = s.emit(msg.ID, "error", map[string]any{"message": "a turn is already running", "recoverable": true})
			return
		}
		workspace, sessionID := s.opts.Workspace, s.session
		ctx, cancel := context.WithCancel(s.ctx)
		s.activeCancel = cancel
		s.active = true
		broker := interaction.NewBroker(ctx, sessionID)
		s.broker = broker
		opts := s.opts
		opts.SessionID = sessionID
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
			s.mu.Lock()
			client := s.adapter
			s.mu.Unlock()
			if client == nil {
				prepare := s.prepareAdapter
				if prepare == nil {
					prepare = prepareAdapter
				}
				var err error
				client, err = prepare(ctx, s.useCodex, s.codexPath)
				if err != nil {
					finished <- turnDone{id: msg.ID, err: err}
					return
				}
				s.mu.Lock()
				if s.adapter == nil {
					s.adapter = client
				} else if client != s.adapter {
					_ = client.Close()
					client = s.adapter
				}
				s.mu.Unlock()
			}
			if err := ctx.Err(); err != nil {
				finished <- turnDone{id: msg.ID, err: err}
				return
			}
			s.mu.Lock()
			opts.SessionID = s.session
			opts.Adapter = client
			s.mu.Unlock()
			out := &rpcRunnerOutput{server: s, id: msg.ID}
			opts.Prompt = text
			opts.Output = out
			opts.JSONL = true
			opts.Diagnostics = s.diagnostics
			opts.DecorateRegistry = func(base tool.Registry) tool.Registry { return interaction.DecorateRegistry(base, broker) }
			opts.OnSession = func(id string) {
				s.mu.Lock()
				s.session = id
				s.opts.SessionID = id
				s.mu.Unlock()
				_ = s.emit(msg.ID, "session", map[string]any{"session_id": id})
			}
			result, err := runner.Run(broker.Context(), opts)
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
	case "cancel":
		s.mu.Lock()
		cancel, active, sessionID, model, effort := s.activeCancel, s.active, s.session, s.opts.Model, s.opts.Effort
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		_ = s.emit(msg.ID, "status", map[string]any{"session_id": sessionID, "model": model, "effort": effort, "cancel_requested": active})
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
		active := s.active
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
		s.session = id
		s.opts.SessionID = id
		workspace, model, effort := s.opts.Workspace, s.opts.Model, s.opts.Effort
		s.mu.Unlock()
		if err := s.emitSessionHistory(msg.ID, id); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "session", map[string]any{"session_id": id})
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": workspace, "session_id": id, "model": model, "effort": effort, "attached": true})
	case "new":
		s.mu.Lock()
		active, sessionID := s.active, s.session
		if !active {
			s.session = ""
			s.opts.SessionID = ""
		}
		s.mu.Unlock()
		if active {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cancel the active turn before starting a new session", "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "ready", map[string]any{"workspace": s.opts.Workspace, "session_id": "", "previous_session_id": sessionID, "model": s.opts.Model, "effort": s.opts.Effort})
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
		if !s.started {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "send start before creating a task", "recoverable": true})
			return
		}
		prompt := get("prompt")
		if strings.TrimSpace(prompt) == "" {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "task prompt is empty", "recoverable": true})
			return
		}
		cfg, _ := config.Load(s.cfgPath)
		model, effort := get("model"), get("effort")
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
		t, err := (tasks.Store{Root: filepath.Join(pkHome(), "tasks")}).Start(s.ctx, tasks.StartOptions{Prompt: prompt, Workspace: workspace, Model: model, Effort: effort, SessionDir: s.sessionDir, SkillsDirs: []string{filepath.Join(userHome(), ".codex", "skills"), filepath.Join(userHome(), ".agents", "skills")}, ToolEvents: true, Executable: executable})
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
		store := tasks.Store{Root: filepath.Join(pkHome(), "tasks")}
		task, err := store.Get(id)
		if err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		if s.taskFollowCancel != nil {
			s.taskFollowCancel()
		}
		followCtx, cancel := context.WithCancel(s.ctx)
		s.taskFollowCancel = cancel
		s.attachedTask = id
		_ = s.emit(msg.ID, "task_attached", taskPayload(task))
		go func() {
			writer := taskOutputWriter{server: s, requestID: msg.ID, taskID: id}
			_, followErr := store.Follow(followCtx, id, 0, writer)
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
		sessionID, model, effort := s.session, s.opts.Model, s.opts.Effort
		s.mu.Unlock()
		_ = s.emit(msg.ID, "status", map[string]any{"session_id": sessionID, "model": model, "effort": effort, "logged_in": status.LoggedIn, "expired": status.Expired, "account_id": status.AccountID})
	case "login":
		if err := auth.Login(s.ctx, auth.LoginOptions{Output: s.diagnostics}); err != nil {
			_ = s.emit(msg.ID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(msg.ID, "status", map[string]any{"logged_in": true})
	case "detach":
		s.mu.Lock()
		active, sessionID := s.active, s.session
		s.mu.Unlock()
		if active {
			_ = s.emit(msg.ID, "error", map[string]any{"message": "cancel the active turn before detaching", "recoverable": true})
			return
		}
		if s.taskFollowCancel != nil {
			s.taskFollowCancel()
			s.taskFollowCancel = nil
			s.attachedTask = ""
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
		model, effort, sessionID := s.opts.Model, s.opts.Effort, s.session
		s.mu.Unlock()
		if model != "" && effort != "" {
			if err := config.Save(s.cfgPath, config.Config{Model: model, Effort: effort}); err != nil {
				_ = s.emit(msg.ID, "error", map[string]any{"message": "could not save model defaults: " + err.Error(), "recoverable": true})
				return
			}
		}
		_ = s.emit(msg.ID, "status", map[string]any{"session_id": sessionID, "model": model, "effort": effort})
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
