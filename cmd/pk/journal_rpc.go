package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/pkyanam/pk/internal/filetools"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

// startJournalRestore runs an idle-guarded journal restore as a foreground
// operation. The report is emitted to the UI and also submitted into the
// session history as a user-visible note so the model sees the restore on the
// next turn.
func (s *rpcServer) startJournalRestore(requestID string, sessionID string, opIDs []string, workspace string) {
	s.mu.Lock()
	if !s.started || s.session == "" {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "attach or start a session before restoring journal entries", "recoverable": true})
		return
	}
	if s.active || s.skillOperationActive || s.pluginCommandActive || s.releaseActive || s.attachedTask != "" || s.reloadPrepared {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "the journal can only be restored while the foreground session is idle", "recoverable": true})
		return
	}
	if sessionID == "" {
		sessionID = s.session
	}
	if sessionID != s.session {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "restore is limited to the attached session", "recoverable": true})
		return
	}
	if strings.TrimSpace(workspace) == "" {
		workspace = s.opts.Workspace
	}
	if strings.TrimSpace(s.opts.WorkspaceJournalRoot) == "" {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "this session has no workspace journal", "recoverable": true})
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.skillOperationActive, s.skillOperationKind = true, "journal_restore"
	s.skillOperationCancel, s.skillOperationDone = cancel, make(chan struct{})
	done := s.skillOperationDone
	journalRoot := s.opts.WorkspaceJournalRoot
	s.mu.Unlock()
	_ = s.emit(requestID, "journal_restore_started", map[string]any{"session_id": sessionID})
	go func() {
		defer close(done)
		defer cancel()
		finishOperation := func() {
			s.mu.Lock()
			if s.skillOperationKind == "journal_restore" && s.skillOperationDone == done {
				s.skillOperationActive, s.skillOperationCancel, s.skillOperationDone, s.skillOperationKind = false, nil, nil, ""
			}
			s.mu.Unlock()
		}
		defer finishOperation()
		lease, err := sessionlock.Acquire(s.sessionDir, sessionID)
		if err != nil {
			_ = s.emit(requestID, "journal_restore_failed", map[string]any{"session_id": sessionID, "message": "session is in use; wait for its active turn to finish before restoring"})
			return
		}
		defer lease.Release()
		store, err := workspacejournal.Open(journalRoot, workspacejournal.Limits{})
		if err != nil {
			_ = s.emit(requestID, "journal_restore_failed", map[string]any{"session_id": sessionID, "message": err.Error()})
			return
		}
		ops, err := store.List(sessionID)
		if err != nil {
			_ = s.emit(requestID, "journal_restore_failed", map[string]any{"session_id": sessionID, "message": err.Error()})
			return
		}
		report, err := store.Restore(ctx, sessionID, workspace, opIDs)
		items := restoredJournalItems(report, ops)
		summary := restoreReportNote(report, workspace, items, err)
		noteRecorded, noteErr := appendJournalNote(context.Background(), s.sessionDir, sessionID, summary)
		if noteErr != nil {
			fmt.Fprintf(s.diagnostics, "pk: record journal restore note: %v\n", noteErr)
		}
		payload := map[string]any{
			"session_id": sessionID, "snapshot": report.Snapshot,
			"restored": uniqueRestoredPaths(items), "restored_operations": items,
			"skipped": skippedSummaries(report), "note_recorded": noteRecorded,
		}
		if err != nil {
			payload["message"] = err.Error()
			payload["partial"] = len(items) > 0
		}
		if noteErr != nil {
			payload["note_error"] = noteErr.Error()
		}
		eventType := "journal_restore_completed"
		if err != nil && len(items) == 0 {
			eventType = "journal_restore_failed"
		}
		if releaseErr := lease.Release(); releaseErr != nil {
			payload["session_unlock_error"] = releaseErr.Error()
		}
		finishOperation()
		_ = s.emit(requestID, eventType, payload)
	}()
}

type restoredJournalItem struct {
	OperationID string `json:"operation_id"`
	Path        string `json:"path,omitempty"`
}

func restoredJournalItems(report workspacejournal.RestoreReport, ops []workspacejournal.Op) []restoredJournalItem {
	paths := make(map[string]string, len(ops))
	for _, op := range ops {
		paths[op.ID] = op.Path
	}
	items := make([]restoredJournalItem, 0, len(report.Restored))
	for _, operationID := range report.Restored {
		items = append(items, restoredJournalItem{OperationID: operationID, Path: paths[operationID]})
	}
	return items
}

func uniqueRestoredPaths(items []restoredJournalItem) []string {
	seen := make(map[string]bool, len(items))
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if item.Path != "" && !seen[item.Path] {
			seen[item.Path] = true
			paths = append(paths, item.Path)
		}
	}
	return paths
}

// restoreReportNote renders the bounded user/model-facing restore note.
func restoreReportNote(report workspacejournal.RestoreReport, workspace string, items []restoredJournalItem, restoreErr error) string {
	var out strings.Builder
	out.WriteString("Journal restore attempt for workspace " + workspace + ".\n")
	if report.SnapshotDir != "" {
		out.WriteString("Safety snapshot: " + report.SnapshotDir + "\n")
	}
	for _, item := range items {
		if item.Path != "" {
			out.WriteString("Restored to pre-tool state: " + item.Path + "\n")
		} else {
			out.WriteString("Restored journal operation: " + item.OperationID + " (path unavailable)\n")
		}
	}
	for _, skip := range report.Skipped {
		out.WriteString("Not restored: " + skip.Path + " (" + skip.Reason + ")\n")
	}
	if len(items) == 0 {
		out.WriteString("No file contents were changed.\n")
	}
	if restoreErr != nil {
		out.WriteString("Restore ended with an error: " + restoreErr.Error() + "\n")
	}
	out.WriteString("File-tool restore only covers WriteFile/EditFile changes; Bash and external edits are unobserved.\n")
	text := out.String()
	if len(text) > 4000 {
		text = text[:4000]
	}
	return text
}

func skippedSummaries(report workspacejournal.RestoreReport) []map[string]any {
	summaries := make([]map[string]any, 0, len(report.Skipped))
	for _, skip := range report.Skipped {
		summaries = append(summaries, map[string]any{"path": skip.Path, "reason": skip.Reason})
	}
	return summaries
}

// appendJournalNote submits a restore note as external input. The caller must
// hold the session lease so a turn cannot race the append. A missing saved
// session is expected for CLI restores of orphaned journal data.
func appendJournalNote(ctx context.Context, sessionDir, sessionID, note string) (bool, error) {
	store, err := localfile.New(sessionDir)
	if err != nil {
		return false, err
	}
	if _, err := store.Resume(ctx, session.ID(sessionID)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	inputID, err := runner.NewSessionID()
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(note)
	if err != nil {
		return false, err
	}
	input := inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: payload}
	if err := store.AppendInput(ctx, session.ID(sessionID), input); err != nil {
		return false, err
	}
	return true, nil
}

// journalStatus answers an RPC request for the attached session's journal.
func (s *rpcServer) journalStatus(requestID string, cursor string, includeDiffs bool, diffPath, diffOpID string) {
	s.mu.Lock()
	if !s.started || s.session == "" {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": "attach or start a session before inspecting the journal", "recoverable": true})
		return
	}
	sessionID := s.session
	journalRoot := s.opts.WorkspaceJournalRoot
	s.mu.Unlock()
	if strings.TrimSpace(journalRoot) == "" {
		_ = s.emit(requestID, "journal", map[string]any{"available": false, "session_id": sessionID})
		return
	}
	store, err := workspacejournal.Open(journalRoot, workspacejournal.Limits{})
	if err != nil {
		_ = s.emit(requestID, "error", map[string]any{"message": err.Error(), "recoverable": true})
		return
	}
	if includeDiffs {
		output, err := filetools.RenderDiff(store, sessionID, diffPath, diffOpID)
		if err != nil {
			_ = s.emit(requestID, "error", map[string]any{"message": err.Error(), "recoverable": true})
			return
		}
		_ = s.emit(requestID, "journal_diff", map[string]any{"session_id": sessionID, "diff": output})
		return
	}
	ops, err := store.List(sessionID)
	if err != nil {
		_ = s.emit(requestID, "error", map[string]any{"message": err.Error(), "recoverable": true})
		return
	}
	summary, err := store.Summarize(sessionID, cursor, workspacejournal.SummaryBounds{MaxPaths: 50})
	if err != nil {
		_ = s.emit(requestID, "error", map[string]any{"message": err.Error(), "recoverable": true})
		return
	}
	_ = s.emit(requestID, "journal", map[string]any{
		"available": true, "session_id": sessionID, "summary": summary, "ops": ops,
		"coverage": "WriteFile/EditFile only; Bash and external edits are unobserved",
	})
}
