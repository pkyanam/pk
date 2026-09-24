package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkyanam/pk/internal/filetools"
	"github.com/pkyanam/pk/internal/runner"
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
	_ = s.emit(requestID, "restore_started", map[string]any{"session_id": sessionID})
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
		store, err := workspacejournal.Open(journalRoot, workspacejournal.Limits{})
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "restore_failed", map[string]any{"message": err.Error()})
			return
		}
		report, err := store.Restore(ctx, sessionID, workspace, opIDs)
		if err != nil {
			finishOperation()
			_ = s.emit(requestID, "restore_failed", map[string]any{"message": err.Error()})
			return
		}
		summary := restoreReportNote(report, workspace)
		_ = s.emit(requestID, "restore_completed", map[string]any{
			"session_id": sessionID, "snapshot": report.Snapshot,
			"restored": report.Restored, "skipped": skippedSummaries(report),
		})
		// Record the restore as a user-visible session note so the model and
		// transcript see it. Submission failures keep the restore itself.
		if err := s.recordJournalNote(sessionID, summary); err != nil {
			fmt.Fprintf(s.diagnostics, "pk: record journal restore note: %v\n", err)
		}
	}()
}

// restoreReportNote renders the bounded user/model-facing restore note.
func restoreReportNote(report workspacejournal.RestoreReport, workspace string) string {
	var out strings.Builder
	out.WriteString("Journal restore applied to workspace " + workspace + ".\n")
	if report.SnapshotDir != "" {
		out.WriteString("Safety snapshot: " + report.SnapshotDir + "\n")
	}
	for _, path := range report.Restored {
		out.WriteString("Restored to pre-tool state: " + path + "\n")
	}
	for _, skip := range report.Skipped {
		out.WriteString("Not restored: " + skip.Path + " (" + skip.Reason + ")\n")
	}
	if len(report.Restored) == 0 {
		out.WriteString("No file contents were changed.\n")
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

// recordJournalNote submits the restore note into the durable session as an
// external input. The note is user-visible in the transcript and enters model
// context on the next turn; it does not itself trigger a turn.
func (s *rpcServer) recordJournalNote(sessionID, note string) error {
	store, err := localfile.New(s.sessionDir)
	if err != nil {
		return err
	}
	inputID, err := runner.NewSessionID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(note)
	if err != nil {
		return err
	}
	input := inbox.Input{ID: inbox.ID(inputID), Kind: inbox.InputExternal, Payload: payload}
	return store.AppendInput(s.ctx, session.ID(sessionID), input)
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
