package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/presentation"
	"github.com/pkyanam/pk/internal/runner"
)

type steeringRequest struct {
	requestID string
	inputID   string
	text      string
	files     []string
	reply     func(error)
	artifacts *steeringArtifactCleanup
}

// steeringArtifactCleanup owns only sidecars created for one unaccepted input.
type steeringArtifactCleanup struct {
	mu      sync.Mutex
	cleanup func()
	cleaned bool
}

func (state *steeringArtifactCleanup) set(cleanup func()) {
	if cleanup == nil {
		return
	}
	state.mu.Lock()
	if state.cleaned {
		state.mu.Unlock()
		cleanup()
		return
	}
	previous := state.cleanup
	state.cleanup = func() {
		if previous != nil {
			previous()
		}
		cleanup()
	}
	state.mu.Unlock()
}

func (state *steeringArtifactCleanup) run() {
	state.mu.Lock()
	if state.cleaned {
		state.mu.Unlock()
		return
	}
	state.cleaned = true
	cleanup := state.cleanup
	state.cleanup = nil
	state.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

// prepareSteeringInputs serializes text and file loading through one FIFO so a
// later text-only steer cannot overtake an earlier attachment extraction.
func (s *rpcServer) prepareSteeringInputs(
	ctx context.Context,
	requests <-chan steeringRequest,
	inputs chan<- runner.Input,
	workspace, sessionDir, initialSessionID string,
	sessionReady <-chan string,
) {
	resolvedSessionID := initialSessionID
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-requests:
			if request.inputID == "" {
				request.reply(fmt.Errorf("steering input ID is empty"))
				continue
			}
			if resolvedSessionID == "" {
				select {
				case resolvedSessionID = <-sessionReady:
				case <-ctx.Done():
					request.artifacts.run()
					request.reply(ctx.Err())
					return
				}
			}
			sessionID := resolvedSessionID
			text := request.text
			var loaded []attachments.Attachment
			if len(request.files) > 0 {
				pageDir := attachments.PromptPDFPageDir(sessionDir, sessionID, request.inputID)
				metaPath := presentation.RecordPath(sessionDir, sessionID, request.inputID)
				duplicate := false
				for _, path := range []string{pageDir, metaPath} {
					if _, err := os.Lstat(path); err == nil {
						request.reply(fmt.Errorf("steering input ID %q already has attachment artifacts; retry with a new request ID", request.inputID))
						duplicate = true
						break
					} else if !os.IsNotExist(err) {
						request.reply(fmt.Errorf("check existing steering attachment artifacts: %w", err))
						duplicate = true
						break
					}
				}
				if duplicate {
					continue
				}
				resolved, err := workspaceAttachmentPaths(workspace, request.files)
				if err != nil {
					request.reply(err)
					continue
				}
				if err := ensurePrivatePageDir(sessionDir, pageDir); err != nil {
					request.reply(fmt.Errorf("prepare attachment storage: %w", err))
					continue
				}
				request.artifacts.set(func() { _ = os.RemoveAll(pageDir) })
				var loadErr error
				if loader := s.loadAttachments; loader != nil {
					text, loaded, loadErr = loader(ctx, workspace, text, resolved)
				} else {
					text, loaded, loadErr = loadPromptAttachmentsWithPageDir(ctx, workspace, text, resolved, pageDir)
				}
				if loadErr != nil {
					request.artifacts.run()
					request.reply(fmt.Errorf("load steering attachments: %w", loadErr))
					continue
				}
				if err := ctx.Err(); err != nil {
					request.artifacts.run()
					request.reply(err)
					return
				}
				if !hasRenderedPDFPages(loaded) {
					_ = os.RemoveAll(pageDir)
					request.artifacts.set(nil)
				}
				if len(loaded) > 0 {
					record, recordErr := presentation.NewRecord(request.text, text, loaded)
					if recordErr != nil {
						fmt.Fprintf(s.diagnostics, "pk: could not describe steering attachment history: %v\n", recordErr)
					} else {
						metaPath := presentation.RecordPath(sessionDir, sessionID, request.inputID)
						_, statErr := os.Lstat(metaPath)
						if saveErr := presentation.Save(sessionDir, sessionID, request.inputID, record); saveErr != nil {
							fmt.Fprintf(s.diagnostics, "pk: could not save steering attachment history; full prompt will be retained: %v\n", saveErr)
						} else if os.IsNotExist(statErr) {
							request.artifacts.set(func() { _ = os.Remove(metaPath) })
						}
					}
				}
				summaries := make([]map[string]any, 0, len(loaded))
				for _, item := range loaded {
					summaries = append(summaries, map[string]any{"path": item.Path, "kind": item.Kind, "content_type": item.ContentType, "truncated": item.Truncated, "pages_extracted": item.PagesExtracted, "pages_total": item.PagesTotal, "rendered_pages": len(item.RenderedPDFPages), "render_notice": item.RenderNotice})
				}
				_ = s.emit(request.requestID, "attachments_loaded", map[string]any{"files": summaries, "input_id": request.inputID, "steering": true})
			}
			input := runner.Input{ID: request.inputID, Text: text, Accepted: func(err error) {
				if err != nil {
					request.artifacts.run()
				}
				request.reply(err)
			}}
			select {
			case inputs <- input:
			case <-ctx.Done():
				request.artifacts.run()
				request.reply(ctx.Err())
				return
			}
		}
	}
}
