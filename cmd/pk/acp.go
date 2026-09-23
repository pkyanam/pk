package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkyanam/pk/internal/acp"
	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// runACPCommand serves ACP v1 over stdio.
func runACPCommand(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int {
	return runACPCommandWithRunnerAdapter(ctx, args, input, output, diagnostics, func(ctx context.Context, options *runner.Options) (llm.Adapter, error) {
		adapter, _, err := prepareCLIAdapter(ctx, options, false, "")
		return adapter, err
	})
}

func runACPCommandWithAdapter(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, prepare func(context.Context) (*codexAdapter, error)) int {
	return runACPCommandWithRunnerAdapter(ctx, args, input, output, diagnostics, func(ctx context.Context, _ *runner.Options) (llm.Adapter, error) {
		return prepare(ctx)
	})
}

func runACPCommandWithRunnerAdapter(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, prepare func(context.Context, *runner.Options) (llm.Adapter, error)) int {
	defaults, err := config.Load(filepathJoin(pkHome(), "config.json"))
	if err != nil {
		fmt.Fprintf(diagnostics, "pk acp: load config: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("acp", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	model := flags.String("model", defaults.Model, "model ID")
	effort := flags.String("effort", defaults.Effort, "reasoning effort")
	providerChoice := flags.String("provider", "", "provider ID or native (default configured provider)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(diagnostics, "pk acp: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	if !config.ValidEffort(*effort) {
		fmt.Fprintf(diagnostics, "pk acp: unsupported reasoning effort %q\n", *effort)
		return 2
	}
	provider, err := resolveCLIProvider(*providerChoice, false)
	if err != nil {
		fmt.Fprintf(diagnostics, "pk acp: select model provider: %v\n", err)
		return 2
	}
	modelSet, effortSet := false, false
	flags.Visit(func(item *flag.Flag) {
		switch item.Name {
		case "model":
			modelSet = true
		case "effort":
			effortSet = true
		}
	})
	selected := runner.Options{Model: *model, Effort: *effort}
	applyProviderDefaults(&selected, provider, modelSet, effortSet)
	sessionDir := filepathJoin(pkHome(), "sessions")
	server, err := acp.NewServer(input, output, acp.Config{Model: selected.Model, Effort: selected.Effort,
		NewSession: func(sessionCtx context.Context, id, _ string) error {
			store, err := localfile.New(sessionDir)
			if err != nil {
				return err
			}
			_, err = store.Create(sessionCtx, session.ID(id))
			return err
		},
		LoadSession: func(sessionCtx context.Context, id, workspace string, emit func(acp.Update) error) error {
			return replayACPSession(sessionCtx, sessionDir, id, workspace, emit)
		},
		Run: func(turnCtx context.Context, turn acp.Turn, emit func(acp.Update) error) (acp.TurnResult, error) {
			options := runner.Options{Prompt: turn.Prompt, SessionID: turn.SessionID, Workspace: turn.Workspace, Model: turn.Model, Effort: turn.Effort, ProviderID: selected.ProviderID, SessionDir: sessionDir, SkillsDirs: defaultSkillDirs(), Output: &acpRunnerWriter{emit: emit, seenTools: make(map[string]bool)}, Diagnostics: diagnostics, JSONL: true}
			client, err := prepare(turnCtx, &options)
			if err != nil {
				return acp.TurnResult{}, err
			}
			if closer, ok := client.(interface{ Close() error }); ok {
				defer closer.Close()
			}
			options.Adapter = client
			var sessionID string
			options.OnSession = func(id string) { sessionID = id }
			result, err := runner.Run(turnCtx, options)
			if result.SessionID != "" {
				sessionID = result.SessionID
			}
			return acp.TurnResult{SessionID: sessionID}, err
		}})
	if err != nil {
		fmt.Fprintf(diagnostics, "pk acp: %v\n", err)
		return 1
	}
	if err := server.Serve(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		fmt.Fprintf(diagnostics, "pk acp: %v\n", err)
		return 1
	}
	return 0
}

func replayACPSession(ctx context.Context, sessionDir, id, workspace string, emit func(acp.Update) error) error {
	lease, err := sessionlock.Acquire(sessionDir, id)
	if err != nil {
		if errors.Is(err, sessionlock.ErrBusy) {
			return fmt.Errorf("session is currently active")
		}
		return fmt.Errorf("lock session for replay: %w", err)
	}
	defer lease.Release()
	store, err := localfile.New(sessionDir)
	if err != nil {
		return err
	}
	if _, err := store.Inspect(ctx, session.ID(id)); err != nil {
		return fmt.Errorf("session is not available")
	}
	savedWorkspace, err := savedACPSessionWorkspace(sessionDir, id)
	if err != nil {
		return err
	}
	resolvedWorkspace, err := filepath.Abs(workspace)
	if err != nil || filepath.Clean(resolvedWorkspace) != filepath.Clean(savedWorkspace) {
		return fmt.Errorf("session belongs to a different workspace")
	}
	var after sessionstore.Sequence
	for {
		page, err := store.Items(ctx, session.ID(id), after, 256)
		if err != nil {
			return fmt.Errorf("read session history: %w", err)
		}
		for _, item := range page.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch item.Kind {
			case sessionstore.ItemInput:
				input, ok := item.Data.(inbox.Input)
				if !ok || input.Kind != inbox.InputExternal {
					continue
				}
				var text string
				if err := json.Unmarshal([]byte(input.Payload), &text); err != nil || text == "" {
					continue
				}
				messageID, err := newACPMessageID()
				if err != nil {
					return err
				}
				if err := emit(acp.Update{Kind: "user", MessageID: messageID, Text: text}); err != nil {
					return err
				}
			case sessionstore.ItemModelResponse:
				response, ok := item.Data.(sessionstore.ModelResponse)
				if !ok {
					continue
				}
				for _, output := range response.Response.Output {
					switch output.Type {
					case llm.ItemMessage:
						message, ok := output.Data.(llm.Message)
						if !ok || message.Role != llm.RoleAssistant || message.Phase == "analysis" || message.Text == "" {
							continue
						}
						messageID, err := newACPMessageID()
						if err != nil {
							return err
						}
						if err := emit(acp.Update{Kind: "assistant", MessageID: messageID, Text: message.Text}); err != nil {
							return err
						}
					case llm.ItemToolCall:
						call, ok := output.Data.(llm.ToolCall)
						if !ok {
							continue
						}
						if err := emit(acp.Update{Kind: "tool_call", ToolCallID: call.CallID, Title: call.Name, ToolKind: acpToolKind(call.Name), Status: "pending"}); err != nil {
							return err
						}
					}
				}
			case sessionstore.ItemToolCallStatus:
				status, ok := item.Data.(sessionstore.ToolCallStatus)
				if !ok {
					continue
				}
				state := acpStatusFromTool(status.Status, status.Operations)
				update := acp.Update{Kind: "tool_call_update", ToolCallID: status.CallID, Status: state}
				if status.Status.Error != "" {
					update.Content = []any{map[string]any{"type": "content", "content": map[string]string{"type": "text", "text": status.Status.Error}}}
				}
				if err := emit(update); err != nil {
					return err
				}
			}
		}
		if !page.More {
			return nil
		}
		after = page.NextAfter
	}
}

func acpStatusFromTool(status tool.CallStatus, operations []operation.Operation) string {
	if status.Error != "" {
		return "failed"
	}
	if len(status.WaitingFor) == 0 {
		return "completed"
	}
	states := make(map[operation.ID]operation.Status, len(operations))
	for _, item := range operations {
		states[item.ID] = item.Status
	}
	for _, id := range status.WaitingFor {
		switch states[id] {
		case operation.StatusFailed:
			return "failed"
		case operation.StatusCanceled:
			return "cancelled"
		case operation.StatusCompleted:
		default:
			return "in_progress"
		}
	}
	return "completed"
}

func savedACPSessionWorkspace(sessionDir, id string) (string, error) {
	digest := sha256.Sum256([]byte(id))
	path := filepath.Join(sessionDir, hex.EncodeToString(digest[:])+".context.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 128<<20 {
		return "", fmt.Errorf("session has no readable saved workspace context")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("session has no readable saved workspace context")
	}
	defer file.Close()
	var snapshot struct {
		Workspace string `json:"Workspace"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 128<<20)).Decode(&snapshot); err != nil || snapshot.Workspace == "" {
		return "", fmt.Errorf("session has no readable saved workspace context")
	}
	return snapshot.Workspace, nil
}

func newACPMessageID() (string, error) {
	var raw [12]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", err
	}
	return "pk_" + hex.EncodeToString(raw[:]), nil
}

// filepathJoin lives here to keep the ACP entry point isolated from main.go.
func filepathJoin(parts ...string) string { return filepath.Join(parts...) }

type acpRunnerWriter struct {
	mu        sync.Mutex
	emit      func(acp.Update) error
	seenTools map[string]bool
	failed    error
	pending   []byte
}

func (w *acpRunnerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			if len(w.pending) > 1<<20 {
				w.pending = nil
				if w.failed == nil {
					w.failed = errors.New("ACP runner event exceeded 1 MiB")
				}
			}
			break
		}
		if newline > 1<<20 {
			if w.failed == nil {
				w.failed = errors.New("ACP runner event exceeded 1 MiB")
			}
			w.pending = w.pending[newline+1:]
			continue
		}
		line := w.pending[:newline]
		w.pending = w.pending[newline+1:]
		var event struct {
			Type       string `json:"type"`
			CallID     string `json:"call_id"`
			Name       string `json:"name"`
			State      string `json:"state"`
			Text       string `json:"text"`
			ResponseID string `json:"response_id"`
			Error      string `json:"error_excerpt"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		switch event.Type {
		case "assistant":
			if event.Text != "" {
				if err := w.emit(acp.Update{Kind: "assistant", MessageID: event.ResponseID, Text: event.Text}); err != nil {
					w.failed = err
				}
			}
		case "tool_call":
			if event.CallID == "" {
				continue
			}
			status := event.State
			if status == "running" {
				status = "in_progress"
			}
			if status == "canceled" {
				status = "cancelled"
			}
			title := event.Name
			if event.Error != "" {
				title += ": " + event.Error
			}
			if !w.seenTools[event.CallID] {
				w.seenTools[event.CallID] = true
				if err := w.emit(acp.Update{Kind: "tool_call", ToolCallID: event.CallID, Title: title, ToolKind: acpToolKind(event.Name), Status: "in_progress"}); err != nil {
					w.failed = err
				}
			}
			if status != "in_progress" {
				u := acp.Update{Kind: "tool_call_update", ToolCallID: event.CallID, Status: status}
				if event.Error != "" {
					u.Content = []any{map[string]any{"type": "content", "content": map[string]string{"type": "text", "text": event.Error}}}
				}
				if err := w.emit(u); err != nil {
					w.failed = err
				}
			}
		}
	}
	if w.failed != nil {
		return len(p), w.failed
	}
	return len(p), nil
}

func acpToolKind(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell", "runcommand":
		return "execute"
	case "viewimage", "readfile", "skilluse":
		return "read"
	case "edit", "writefile", "applypatch":
		return "edit"
	case "search":
		return "search"
	default:
		return "other"
	}
}
