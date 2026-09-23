// Package runner composes the public Unreal Agent runtime into a single-user
// terminal session. The coordinator remains responsible for scheduling,
// operation persistence, recovery, and cancellation.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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
	Prompt       string
	SessionID    string
	SessionDir   string
	Workspace    string
	Model        string
	Effort       string
	SystemPrompt string
	SkillsDirs   []string
	JSONL        bool
	ToolEvents   bool
	Output       io.Writer
	Diagnostics  io.Writer
	OnSession    func(string)
	Store        sessionstore.Store
	Adapter      llm.Adapter
}

// RunResult reports the durable session ID and any final assistant text emitted.
type RunResult struct {
	SessionID string
	Text      string
}

// Run executes one prompt, resuming a prior session when SessionID is set.
// Canceling ctx sends a hard-stop control to the coordinator and lets it settle
// or cancel active operations before returning.
func Run(ctx context.Context, options Options) (RunResult, error) {
	if strings.TrimSpace(options.Prompt) == "" {
		return RunResult{}, errors.New("prompt must not be empty")
	}
	if options.Adapter == nil {
		return RunResult{}, errors.New("LLM adapter is required")
	}
	if options.Model == "" {
		options.Model = "gpt-6-astra"
	}
	if options.Effort == "" {
		options.Effort = "xhigh"
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
	if options.SessionID == "" {
		newID, err := newID()
		if err != nil {
			return RunResult{}, err
		}
		id = session.ID(newID)
		if _, err := store.Create(ctx, id); err != nil {
			return RunResult{}, fmt.Errorf("create session: %w", err)
		}
	} else {
		id = session.ID(options.SessionID)
		var err error
		restored, err = store.Resume(ctx, id)
		if err != nil {
			return RunResult{}, fmt.Errorf("resume session %q: %w", id, err)
		}
	}
	if options.SessionID == "" {
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
	manager := operation.NewLocalOperationManager(runCtx)
	defer func() {
		stopRun()
		for range manager.Updates() {
			// Drain buffered status updates while the manager shuts down. The
			// coordinator has already returned, so no consumer remains attached.
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
	registry, skills, warnings := newToolRegistry(options.Workspace, operationDir, options.SkillsDirs)
	for _, warning := range warnings {
		fmt.Fprintf(options.Diagnostics, "pk: warning: %v\n", warning)
	}
	builder := contextbuilder.NewBuilder(skills...)
	builder.SetModel(llm.Model{ID: options.Model, ReasoningEffort: effort})
	builder.SetSystemPrompt(workspaceSystemPrompt(options.Workspace, options.SystemPrompt, options.Diagnostics))
	for _, definition := range registry.StaticDefinitions() {
		builder.AddTool(definition.Tool)
	}
	var emitted strings.Builder
	var emittedMu sync.Mutex
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
	observer := outputObserver(options.Output, options.Diagnostics, options.JSONL, options.ToolEvents, runCtx, inputs, &emitted, &emittedMu, recordOutputErr)
	observerID := store.AddObserver(observer)
	defer store.RemoveObserver(observerID)
	current := coordinator.New(coordinator.Dependencies{
		SessionID: id, Inbox: inputs, Restored: restored, Sessions: store,
		ContextBuilder: builder, LLM: options.Adapter, Tools: registry,
		Operations: manager,
	})
	done := make(chan error, 1)
	go func() { done <- current.Run(runCtx) }()
	inputID, err := newID()
	if err != nil {
		stopRun()
		<-done
		return RunResult{}, err
	}
	payload, err := json.Marshal(options.Prompt)
	if err != nil {
		stopRun()
		<-done
		return RunResult{}, fmt.Errorf("encode prompt: %w", err)
	}
	if err := inputs.Submit(ctx, inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: payload}); err != nil {
		stopRun()
		<-done
		return RunResult{}, fmt.Errorf("submit prompt: %w", err)
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

func outputObserver(out, diagnostics io.Writer, jsonl, toolEvents bool, runCtx context.Context, inputs *inbox.Inbox, emitted *strings.Builder, emittedMu *sync.Mutex, recordOutputErr func(error)) sessionstore.Observer {
	var mu sync.Mutex
	toolNames := make(map[string]string)
	return func(id session.ID, item sessionstore.Item) {
		if item.Kind == sessionstore.ItemModelResponse {
			response := item.Data.(sessionstore.ModelResponse).Response
			for _, output := range response.Output {
				if output.Type == llm.ItemToolCall {
					call := output.Data.(llm.ToolCall)
					toolNames[call.CallID] = call.Name
				}
			}
		} else if item.Kind == sessionstore.ItemToolCallStatus {
			status := item.Data.(sessionstore.ToolCallStatus)
			if toolEvents {
				name := toolNames[status.CallID]
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
				recordOutputErr(emitJSONLine(&mu, out, map[string]any{"type": "tool_call", "session_id": id, "call_id": status.CallID, "status": status.Status}))
			}
			return
		} else {
			return
		}
		response := item.Data.(sessionstore.ModelResponse).Response
		var textParts []string
		hasToolCall := false
		for _, output := range response.Output {
			switch output.Type {
			case llm.ItemMessage:
				message, ok := output.Data.(llm.Message)
				if ok && message.Role == llm.RoleAssistant && message.Phase != "analysis" && message.Text != "" {
					textParts = append(textParts, message.Text)
				}
			case llm.ItemToolCall:
				hasToolCall = true
			}
		}
		text := strings.Join(textParts, "\n")
		if text != "" {
			emittedMu.Lock()
			emitted.WriteString(text)
			emitted.WriteByte('\n')
			emittedMu.Unlock()
			mu.Lock()
			if jsonl {
				recordOutputErr(writeJSONLine(out, map[string]any{"type": "assistant", "session_id": id, "text": text, "response_id": response.ID}))
			} else {
				_, err := fmt.Fprintln(out, text)
				recordOutputErr(err)
			}
			mu.Unlock()
		}
		if !hasToolCall {
			// Submit outside the observer: store callbacks are synchronous while the
			// coordinator is processing this event, so a direct Submit would deadlock.
			go func() { _ = submitStop(runCtx, inputs, inbox.StopWhenIdle, "assistant turn complete") }()
		}
	}
}

func isTerminal(status operation.Status) bool {
	return status == operation.StatusCompleted || status == operation.StatusFailed || status == operation.StatusCanceled
}

const maxWorkspaceInstructions = 64 << 10

func workspaceSystemPrompt(workspace, explicit string, diagnostics io.Writer) string {
	sections := []string{
		"You are pk, a local coding agent working in the user's current project. Use Bash to inspect, edit, and verify files in the supplied workspace. Explore relevant code before changing it, keep edits focused, and report what changed and what you verified without claiming checks that did not run. Ask only when missing information blocks safe progress. Do not expose credentials or other secrets. Before editing a nested path, inspect and follow the nearest nested AGENTS.md; pk automatically loads only the workspace-root AGENTS.md.\n\nWorkspace: " + workspace + "\nPlatform: " + runtime.GOOS + "/" + runtime.GOARCH + "; shell: /bin/sh.",
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
	registry := tool.NewRegistry(tool.StaticTranslators{
		Bash:      bash.New(bash.Config{Shell: "/bin/sh", Directory: workspace, BaseDirectory: operationDir}),
		ViewImage: viewimage.New(viewimage.Config{Directory: workspace}),
	}, tool.BashName, tool.ViewImageName, tool.SkillUseName)
	var skills []tool.Skill
	var warnings []error
	for _, directory := range dirs {
		discovered, errs := tool.DiscoverSkills(directory)
		skills = append(skills, discovered...)
		warnings = append(warnings, errs...)
	}
	for _, skill := range skills {
		if _, err := registry.RegisterSkill(skill); err != nil {
			warnings = append(warnings, err)
		}
	}
	return registry, registry.Skills(), warnings
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
