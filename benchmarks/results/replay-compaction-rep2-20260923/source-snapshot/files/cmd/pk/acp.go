package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pkyanam/pk/internal/acp"
	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/runner"
)

// runACPCommand serves ACP v1 over stdio.
func runACPCommand(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int {
	return runACPCommandWithAdapter(ctx, args, input, output, diagnostics, func(ctx context.Context) (*codexAdapter, error) { return prepareAdapter(ctx, false, "") })
}

func runACPCommandWithAdapter(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer, prepare func(context.Context) (*codexAdapter, error)) int {
	defaults, err := config.Load(filepathJoin(pkHome(), "config.json"))
	if err != nil {
		fmt.Fprintf(diagnostics, "pk acp: load config: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("acp", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	model := flags.String("model", defaults.Model, "model ID")
	effort := flags.String("effort", defaults.Effort, "reasoning effort")
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
	server, err := acp.NewServer(input, output, acp.Config{Model: *model, Effort: *effort, Run: func(turnCtx context.Context, turn acp.Turn, emit func(acp.Update) error) (acp.TurnResult, error) {
		client, err := prepare(turnCtx)
		if err != nil {
			return acp.TurnResult{}, err
		}
		defer client.Close()
		writer := &acpRunnerWriter{emit: emit, seenTools: make(map[string]bool)}
		options := runner.Options{Prompt: turn.Prompt, SessionID: turn.SessionID, Workspace: turn.Workspace, Model: turn.Model, Effort: turn.Effort, SessionDir: filepathJoin(pkHome(), "sessions"), SkillsDirs: defaultSkillDirs(), Adapter: client, Output: writer, Diagnostics: diagnostics, JSONL: true}
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

// filepathJoin lives here to keep the ACP entry point isolated from main.go.
func filepathJoin(parts ...string) string { return filepath.Join(parts...) }

type acpRunnerWriter struct {
	mu        sync.Mutex
	emit      func(acp.Update) error
	seenTools map[string]bool
	failed    error
}

func (w *acpRunnerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	scanner := bufio.NewScanner(strings.NewReader(string(p)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event struct {
			Type       string `json:"type"`
			CallID     string `json:"call_id"`
			Name       string `json:"name"`
			State      string `json:"state"`
			Text       string `json:"text"`
			ResponseID string `json:"response_id"`
			Error      string `json:"error_excerpt"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
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
	if err := scanner.Err(); err != nil {
		return len(p), err
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
