package filetools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/pkyanam/pk/internal/workspacejournal/diff"
)

// maxDeltaDiffBytes bounds the model-visible diff returned by WorkspaceDelta.
const maxDeltaDiffBytes = 16 << 10

// deltaArgs validates WorkspaceDelta arguments.
type deltaArgs struct {
	Cursor string `json:"cursor,omitempty"`
	Path   string `json:"path,omitempty"`
	Diff   bool   `json:"diff,omitempty"`
	OpID   string `json:"op_id,omitempty"`
}

// validateDeltaArgs checks WorkspaceDelta arguments explicitly so the schema
// stays permissive while malformed combinations fail with a clear message.
func validateDeltaArgs(value deltaArgs) error {
	if strings.ContainsRune(value.Cursor, '\x00') || len(value.Cursor) > 32 {
		return errors.New("cursor must be a decimal sequence number under 32 bytes")
	}
	for _, r := range value.Cursor {
		if r < '0' || r > '9' {
			return errors.New("cursor must be a decimal sequence number")
		}
	}
	if len(value.Path) > 4096 || strings.ContainsRune(value.Path, '\x00') {
		return errors.New("path must be a workspace path under 4096 bytes")
	}
	if len(value.OpID) > 128 || strings.ContainsRune(value.OpID, '\x00') {
		return errors.New("op_id must be an operation ID under 128 bytes")
	}
	return nil
}

// runDeltaRequest answers a raw WorkspaceDelta request; it is the shared
// entry point for the remote-job worker and tests.
func runDeltaRequest(store *workspacejournal.Store, session, raw string) (string, error) {
	return runDelta(store, session, raw)
}

// runDelta answers a WorkspaceDelta call. Summaries are the default; diffs
// require an explicit path or operation ID.
func runDelta(store *workspacejournal.Store, session, raw string) (string, error) {
	var parsed deltaArgs
	if raw != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&parsed); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return "", errors.New("arguments must contain one JSON value")
			}
			return "", errors.New("arguments must contain one JSON value")
		}
	}
	if err := validateDeltaArgs(parsed); err != nil {
		return "", err
	}
	if parsed.Diff {
		if parsed.Path == "" && parsed.OpID == "" {
			return "", errors.New("diff=true requires path or op_id")
		}
		return renderDiffAsOf(store, session, parsed.Path, parsed.OpID, parsed.Cursor)
	}
	if parsed.Path != "" || parsed.OpID != "" {
		return "", errors.New("path and op_id require diff=true")
	}
	summary, err := store.Summarize(session, parsed.Cursor, workspacejournal.SummaryBounds{MaxPaths: 50})
	if err != nil {
		return "", err
	}
	formatted := workspacejournal.FormatSummary(summary, "")
	if parsed.Cursor != "" {
		formatted = strings.Replace(formatted, " since cursor "+parsed.Cursor, " as of cursor "+parsed.Cursor, 1)
	}
	if summary.Cursor != "" {
		formatted = strings.TrimRight(formatted, "\n") + fmt.Sprintf("\nJournal cursor: %s (inclusive as-of sequence; omit cursor for the latest summary).\n", summary.Cursor)
	}
	return formatted, nil
}

// RenderDiff exposes one bounded journal diff for CLI and RPC callers.
func RenderDiff(store *workspacejournal.Store, session, path, opID string) (string, error) {
	return renderDiff(store, session, path, opID)
}

// renderDiff renders a bounded unified diff for one path's latest recorded
// mutation or one specific operation.
func renderDiff(store *workspacejournal.Store, session, path, opID string) (string, error) {
	return renderDiffAsOf(store, session, path, opID, "")
}

func renderDiffAsOf(store *workspacejournal.Store, session, path, opID, cursor string) (string, error) {
	var cutoff uint64
	if cursor != "" {
		parsed, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid cursor %q: expected a decimal sequence number", cursor)
		}
		cutoff = parsed
	}
	ops, err := store.List(session)
	if err != nil {
		return "", err
	}
	var chosen *workspacejournal.Op
	for index := len(ops) - 1; index >= 0; index-- {
		op := ops[index]
		if cursor != "" && op.Seq > cutoff {
			continue
		}
		if opID != "" && op.ID == opID {
			chosen = &ops[index]
			break
		}
		if opID == "" && path != "" && op.Path == path && (op.Status == workspacejournal.StatusCompleted || op.Status == workspacejournal.StatusRestored) {
			chosen = &ops[index]
			break
		}
	}
	if chosen == nil {
		if opID != "" {
			if cursor != "" {
				return "", fmt.Errorf("no retained journal entry for operation %q at or before cursor %q", opID, cursor)
			}
			return "", fmt.Errorf("no retained journal entry for operation %q", opID)
		}
		if cursor != "" {
			return "", fmt.Errorf("no retained completed mutation recorded for path %q at or before cursor %q", path, cursor)
		}
		return "", fmt.Errorf("no retained completed mutation recorded for path %q", path)
	}
	// The most recent diffable state for this path: compare the chosen
	// operation's post state against its own preimage (per-operation diff).
	if chosen.Post == nil || chosen.Pre == nil {
		return "", errors.New("this operation cannot be diffed (created file, failed, or missing images)")
	}
	before, err := store.ReadObject(*chosen.Pre)
	if err != nil {
		return "", err
	}
	after, err := store.ReadObject(*chosen.Post)
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("--- a/%s\n+++ b/%s (journal seq %d, %s)", chosen.Path, chosen.Path, chosen.Seq, chosen.Status)
	return diff.Unified(header, string(before), string(after), diff.Options{MaxOutputBytes: maxDeltaDiffBytes}), nil
}
