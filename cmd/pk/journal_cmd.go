package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/filetools"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/pkyanam/pk/internal/workspacejournal"
)

func journalStore() (*workspacejournal.Store, error) {
	root := filepath.Join(pkHome(), "journal")
	store, err := workspacejournal.Open(root, workspacejournal.Limits{})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// runJournalCommand implements pk journal list|diff|restore. It reads the
// caller-supplied session ID and optional workspace; restore always requires
// the workspace and an explicit --yes confirmation.
func runJournalCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stderr, "usage: pk journal list SESSION [--json] | pk journal diff SESSION --path PATH [--op-id ID] | pk journal plan SESSION [--op ID]... --workspace DIR | pk journal restore SESSION [--op ID]... --workspace DIR --yes")
		return 2
	}
	subcommand := args[0]
	rest := args[1:]
	store, err := journalStore()
	if err != nil {
		fmt.Fprintf(stderr, "pk journal: %v\n", err)
		return 1
	}
	switch subcommand {
	case "list":
		return runJournalList(store, rest, stdout, stderr)
	case "diff":
		return runJournalDiff(store, rest, stdout, stderr)
	case "plan":
		return runJournalPlan(store, ctx, rest, stdout, stderr)
	case "restore":
		return runJournalRestore(store, ctx, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "pk journal: unknown subcommand %q\n", subcommand)
		return 2
	}
}

func journalFlags(name string, rest []string, stderr io.Writer) (*flag.FlagSet, *string, *stringList) {
	flags := flag.NewFlagSet("pk journal "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace directory the session ran in")
	var opIDs stringList
	flags.Var(&opIDs, "op", "journal operation ID; may be repeated")
	if err := flags.Parse(rest); err != nil {
		return nil, nil, nil
	}
	return flags, workspace, &opIDs
}

func runJournalList(store *workspacejournal.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk journal list SESSION [--json]")
		return 2
	}
	session := args[0]
	flags := flag.NewFlagSet("pk journal list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	ops, err := store.List(session)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal list: %v\n", err)
		return 1
	}
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(ops); err != nil {
			fmt.Fprintf(stderr, "pk journal list: %v\n", err)
			return 1
		}
		return 0
	}
	printJournalOps(stdout, ops)
	return 0
}

func printJournalOps(stdout io.Writer, ops []workspacejournal.Op) {
	if len(ops) == 0 {
		fmt.Fprintln(stdout, "No file-tool changes recorded. Bash and external edits are unobserved.")
		return
	}
	for _, op := range ops {
		detail := ""
		if op.Reason != "" {
			detail = " (" + op.Reason + ")"
		}
		fmt.Fprintf(stdout, "seq %-4d %-9s %-8s %-10s %s%s\n", op.Seq, op.Status, op.Action, shortOpID(op.ID), op.Path, detail)
	}
}

func shortOpID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}

func runJournalDiff(store *workspacejournal.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk journal diff SESSION --path PATH [--op-id ID]")
		return 2
	}
	session := args[0]
	flags := flag.NewFlagSet("pk journal diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("path", "", "workspace-relative path of the latest recorded mutation")
	opID := flags.String("op-id", "", "specific journal operation ID")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *path == "" && *opID == "" {
		fmt.Fprintln(stderr, "pk journal diff: --path or --op-id is required")
		return 2
	}
	output, err := runDiffRequest(store, session, *path, *opID)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal diff: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, output)
	return 0
}

func runJournalPlan(store *workspacejournal.Store, ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk journal plan SESSION [--op ID]... --workspace DIR")
		return 2
	}
	session := args[0]
	flags, workspace, opIDs := journalFlags("plan", args[1:], stderr)
	if flags == nil {
		return 2
	}
	if strings.TrimSpace(*workspace) == "" {
		fmt.Fprintln(stderr, "pk journal plan: --workspace is required")
		return 2
	}
	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal plan: %v\n", err)
		return 1
	}
	actions, skipped, err := store.PlanRestore(ctx, session, absWorkspace, *opIDs)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal plan: %v\n", err)
		return 1
	}
	printRestorePlan(stdout, actions, skipped)
	return 0
}

func runJournalRestore(store *workspacejournal.Store, ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk journal restore SESSION [--op ID]... --workspace DIR --yes")
		return 2
	}
	session := args[0]
	flags := flag.NewFlagSet("pk journal restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace directory the session ran in")
	var opIDs stringList
	flags.Var(&opIDs, "op", "journal operation ID; may be repeated (default: all non-superseded mutations)")
	yes := flags.Bool("yes", false, "confirm the restore without a prompt")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if strings.TrimSpace(*workspace) == "" {
		fmt.Fprintln(stderr, "pk journal restore: --workspace is required")
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "pk journal restore: refusing to restore without --yes; run `pk journal plan` first to inspect the changes")
		return 2
	}
	absWorkspace, err := filepath.Abs(*workspace)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal restore: %v\n", err)
		return 1
	}
	sessionDir := filepath.Join(pkHome(), "sessions")
	lease, err := sessionlock.Acquire(sessionDir, session)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal restore: cannot safely restore this session: %v\n", err)
		return 1
	}
	defer lease.Release()
	ops, err := store.List(session)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal restore: %v\n", err)
		return 1
	}
	report, err := store.Restore(ctx, session, absWorkspace, opIDs)
	items := restoredJournalItems(report, ops)
	note := restoreReportNote(report, absWorkspace, items, err)
	noteRecorded, noteErr := appendJournalNote(context.Background(), sessionDir, session, note)
	printRestoreReport(stdout, report, items)
	if err != nil {
		fmt.Fprintf(stderr, "pk journal restore: %v\n", err)
	}
	if noteErr != nil {
		fmt.Fprintf(stderr, "pk journal restore: file restore report could not be added to the saved session: %v\n", noteErr)
	} else if noteRecorded {
		fmt.Fprintln(stdout, "Restore report added to the saved session.")
	} else {
		fmt.Fprintln(stdout, "No saved session was found; the restore report was not added to session history.")
	}
	if err != nil || noteErr != nil || len(items) == 0 {
		return 1
	}
	return 0
}

func printRestorePlan(stdout io.Writer, actions []workspacejournal.RestoreAction, skipped []workspacejournal.SkippedRestore) {
	if len(actions) == 0 {
		fmt.Fprintln(stdout, "Nothing to restore.")
	}
	for _, action := range actions {
		kind := "rewrite with recorded preimage"
		if action.Kind == "delete" {
			kind = "delete created file"
		}
		fmt.Fprintf(stdout, "would %s: %s (op %s)\n", kind, action.Path, shortOpID(action.OpID))
	}
	for _, skip := range skipped {
		fmt.Fprintf(stdout, "would skip: %s (op %s): %s\n", skip.Path, shortOpID(skip.OpID), skip.Reason)
	}
}

func printRestoreReport(stdout io.Writer, report workspacejournal.RestoreReport, items []restoredJournalItem) {
	if report.Snapshot != "" {
		fmt.Fprintf(stdout, "Safety snapshot: %s\n", report.SnapshotDir)
	}
	for _, item := range items {
		if item.Path == "" {
			fmt.Fprintf(stdout, "restored operation %s (path unavailable)\n", shortOpID(item.OperationID))
			continue
		}
		fmt.Fprintf(stdout, "restored: %s (op %s)\n", item.Path, shortOpID(item.OperationID))
	}
	for _, skip := range report.Skipped {
		fmt.Fprintf(stdout, "skipped: %s: %s\n", skip.Path, skip.Reason)
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "No changes were applied.")
	}
}

// runDiffRequest renders one bounded journal diff for CLI output.
func runDiffRequest(store *workspacejournal.Store, session, path, opID string) (string, error) {
	return filetools.RenderDiff(store, session, path, opID)
}
