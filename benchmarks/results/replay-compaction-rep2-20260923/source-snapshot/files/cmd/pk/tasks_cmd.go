package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/tasks"
)

func taskStore() tasks.Store { return tasks.Store{Root: filepath.Join(pkHome(), "tasks")} }

func runTaskCommand(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: pk task create|list|status|attach|cancel|resume ...")
		return 2
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("pk task create", flag.ContinueOnError)
		fs.SetOutput(errOut)
		var prompt, workspace, model, effort, system string
		fs.StringVar(&prompt, "p", "", "prompt to run")
		fs.StringVar(&prompt, "prompt", "", "prompt to run")
		fs.StringVar(&workspace, "workspace", "", "new or existing task workspace directory")
		fs.StringVar(&model, "model", "", "model override")
		fs.StringVar(&effort, "effort", "", "reasoning effort override")
		fs.StringVar(&system, "system", "", "additional system instructions")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if fs.NArg() != 0 || strings.TrimSpace(prompt) == "" {
			fmt.Fprintln(errOut, "usage: pk task create -p PROMPT [--workspace DIR] [--model MODEL --effort EFFORT]")
			return 2
		}
		if workspace == "" {
			workspace, _ = os.Getwd()
		}
		workspace, err := filepath.Abs(workspace)
		if err != nil {
			fmt.Fprintf(errOut, "pk task create: %v\n", err)
			return 1
		}
		cfg, err := config.Load(filepath.Join(pkHome(), "config.json"))
		if err != nil {
			fmt.Fprintf(errOut, "pk task create: %v\n", err)
			return 1
		}
		if model == "" {
			model = cfg.Model
		}
		if effort == "" {
			effort = cfg.Effort
		}
		if !config.ValidEffort(effort) {
			fmt.Fprintf(errOut, "pk task create: unsupported effort %q\n", effort)
			return 2
		}
		effort = strings.ToLower(effort)
		executable, err := os.Executable()
		if err != nil {
			fmt.Fprintf(errOut, "pk task create: %v\n", err)
			return 1
		}
		t, err := taskStore().Start(ctx, tasks.StartOptions{Prompt: prompt, Workspace: workspace, Model: model, Effort: effort, SystemPrompt: system, SessionDir: filepath.Join(pkHome(), "sessions"), SkillsDirs: []string{filepath.Join(userHome(), ".codex", "skills"), filepath.Join(userHome(), ".agents", "skills")}, ToolEvents: true, Executable: executable})
		if err != nil {
			fmt.Fprintf(errOut, "pk task create: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "%s\t%s\t%s\n", t.ID, t.Status, t.Workspace)
		return 0
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(errOut, "usage: pk task list")
			return 2
		}
		list, err := taskStore().List()
		if err != nil {
			fmt.Fprintf(errOut, "pk task list: %v\n", err)
			return 1
		}
		for _, t := range list {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", t.ID, t.Status, t.Model+"/"+t.Effort, t.Prompt)
		}
		return 0
	case "status":
		if len(args) != 2 {
			fmt.Fprintln(errOut, "usage: pk task status ID")
			return 2
		}
		t, err := taskStore().Get(args[1])
		if err != nil {
			fmt.Fprintf(errOut, "pk task status: %v\n", err)
			return 1
		}
		b, _ := json.MarshalIndent(t, "", "  ")
		fmt.Fprintln(out, string(b))
		return 0
	case "attach":
		if len(args) < 2 || len(args) > 3 {
			fmt.Fprintln(errOut, "usage: pk task attach ID [EVENT_CURSOR]")
			return 2
		}
		var after uint64
		if len(args) == 3 {
			if _, err := fmt.Sscan(args[2], &after); err != nil {
				fmt.Fprintf(errOut, "pk task attach: invalid event cursor: %v\n", err)
				return 2
			}
		}
		if _, err := taskStore().Follow(ctx, args[1], after, out); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(errOut, "pk task attach: %v\n", err)
			return 1
		}
		return 0
	case "cancel":
		if len(args) != 2 {
			fmt.Fprintln(errOut, "usage: pk task cancel ID")
			return 2
		}
		if err := taskStore().Cancel(args[1]); err != nil {
			fmt.Fprintf(errOut, "pk task cancel: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "Cancel requested for %s.\n", args[1])
		return 0
	case "resume", "start":
		if len(args) != 2 {
			fmt.Fprintf(errOut, "usage: pk task %s ID\n", args[0])
			return 2
		}
		t, err := taskStore().Resume(ctx, args[1])
		if err != nil {
			fmt.Fprintf(errOut, "pk task %s: %v\n", args[0], err)
			return 1
		}
		fmt.Fprintf(out, "%s\t%s\n", t.ID, t.Status)
		return 0
	default:
		fmt.Fprintf(errOut, "pk task: unknown command %q\n", args[0])
		return 2
	}
}

func runTaskWorker(ctx context.Context, args []string, diagnostics io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(diagnostics, "pk: invalid task worker arguments")
		return 2
	}
	err := tasks.RunWorker(ctx, args[0], func(ctx context.Context, taskOptions tasks.WorkerOptions, output io.Writer) error {
		client, err := prepareAdapter(ctx, taskOptions.UseCodex, taskOptions.CodexPath)
		if err != nil {
			return err
		}
		defer client.Close()
		inputs := make(chan runner.Input, 32)
		go func() {
			defer close(inputs)
			for input := range taskOptions.Inputs {
				select {
				case inputs <- runner.Input{ID: input.ID, Text: input.Text, Accepted: input.Ack}:
				case <-ctx.Done():
					if input.Ack != nil {
						input.Ack(ctx.Err())
					}
					return
				}
			}
		}()
		o := runner.Options{Prompt: taskOptions.Prompt, PromptID: taskOptions.PromptID, SessionID: taskOptions.SessionID, Workspace: taskOptions.Workspace, Model: taskOptions.Model, Effort: taskOptions.Effort, SystemPrompt: taskOptions.SystemPrompt, SessionDir: taskOptions.SessionDir, SkillsDirs: taskOptions.SkillsDirs, JSONL: taskOptions.JSONL, ToolEvents: taskOptions.ToolEvents, Output: output, Diagnostics: diagnostics, Adapter: client, OnSession: taskOptions.OnSession, Inputs: inputs, KeepAlive: taskOptions.KeepAlive}
		mcpHost, err := configureCLIMCP(ctx, &o, diagnostics)
		if err != nil {
			return err
		}
		_, err = runner.Run(ctx, o)
		if mcpHost != nil {
			if closeErr := mcpHost.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}
		return err
	})
	if err != nil {
		fmt.Fprintf(diagnostics, "pk task worker: %v\n", err)
		return 1
	}
	return 0
}
