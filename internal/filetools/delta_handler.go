package filetools

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"sync"

	"github.com/pkyanam/pk/internal/workspacejournal"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// deltaHandler executes read-only WorkspaceDelta operations against the
// journal. It has no workspace write path.
type deltaHandler struct {
	ctx       context.Context
	store     *workspacejournal.Store
	sessionID string
	mu        sync.Mutex
	jobs      map[operation.ID]*deltaJob
	updates   chan operation.Operation
	wg        sync.WaitGroup
	done      chan struct{}
}

type deltaJob struct {
	operation operation.Operation
	cancel    context.CancelFunc
	canceled  bool
}

func newDeltaHandler(ctx context.Context, store *workspacejournal.Store, sessionID string) *deltaHandler {
	h := &deltaHandler{ctx: ctx, store: store, sessionID: sessionID, jobs: make(map[operation.ID]*deltaJob), updates: make(chan operation.Operation, 32), done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		for _, job := range h.jobs {
			job.cancel()
		}
		h.mu.Unlock()
		h.wg.Wait()
		close(h.updates)
		close(h.done)
	}()
	return h
}

func (*deltaHandler) RemoteJobPlanType() operation.RemoteJobPlanType       { return deltaPlanType }
func (*deltaHandler) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return deltaPlanVersion }
func (h *deltaHandler) RemoteJobUpdates() <-chan operation.Operation       { return h.updates }
func (h *deltaHandler) Wait()                                              { <-h.done }

func (h *deltaHandler) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err
	}
	if state.Plan.Type != deltaPlanType || state.Plan.Version != deltaPlanVersion {
		return errors.New("unsupported workspace-delta operation")
	}
	// The manager registers the operation as ready; publish the awaiting
	// snapshot so the later completed update is a valid ready→awaiting→
	// completed transition.
	awaiting, err := operation.UpdateRemoteJob(op, state, operation.StatusAwaiting)
	if err != nil {
		return err
	}
	op = *awaiting.Operation
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		return h.ctx.Err()
	}
	if _, exists := h.jobs[op.ID]; exists {
		h.mu.Unlock()
		return nil
	}
	jobCtx, cancel := context.WithCancel(h.ctx)
	job := &deltaJob{operation: op, cancel: cancel}
	h.jobs[op.ID] = job
	h.wg.Add(1)
	h.mu.Unlock()
	if err := h.publish(op); err != nil {
		h.mu.Lock()
		delete(h.jobs, op.ID)
		h.mu.Unlock()
		cancel()
		h.wg.Done()
		return err
	}
	go h.execute(jobCtx, op.ID, job, string(state.Plan.Data))
	return nil
}

func (h *deltaHandler) CancelRemoteJob(id operation.ID, _ string) error {
	h.mu.Lock()
	if job := h.jobs[id]; job != nil {
		job.canceled = true
		job.cancel()
	}
	h.mu.Unlock()
	return nil
}

func (h *deltaHandler) publish(op operation.Operation) error {
	select {
	case h.updates <- op:
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

func (h *deltaHandler) execute(ctx context.Context, id operation.ID, j *deltaJob, raw string) {
	defer h.wg.Done()
	result, err := runDelta(h.store, h.sessionID, raw)
	h.mu.Lock()
	canceled := (j.canceled || ctx.Err() != nil) && err != nil
	delete(h.jobs, id)
	h.mu.Unlock()
	var step operation.Step
	if canceled {
		step, err = operation.CancelRemoteJob(j.operation)
	} else if err != nil {
		step, err = operation.FailRemoteJob(j.operation, err)
	} else {
		state, stateErr := operation.DecodeRemoteJobState(j.operation)
		if stateErr != nil {
			step, err = operation.FailRemoteJob(j.operation, stateErr)
		} else {
			state.TerminalResult = result
			step, err = operation.UpdateRemoteJob(j.operation, state, operation.StatusCompleted)
		}
	}
	if err == nil && step.Operation != nil {
		_ = h.publish(*step.Operation)
	}
}

// deltaTranslator submits WorkspaceDelta calls to the delta handler.
type deltaTranslator struct{}

func (deltaTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	if ctx == nil {
		return tool.CallStatus{Error: "workspace-delta context unavailable"}
	}
	if len(call.Arguments) > maxArgumentBytes || (len(call.Arguments) > 0 && !json.Valid([]byte(call.Arguments))) {
		return tool.CallStatus{Error: "workspace-delta arguments must be valid JSON under 16 MiB"}
	}
	data := jsontext.Value(call.Arguments)
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: deltaPlanType, Version: deltaPlanVersion, Data: data})
	if err != nil {
		return tool.CallStatus{Error: "could not create workspace-delta operation"}
	}
	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}

func (deltaTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		return textResult(callID, "Error: "+status.Error), nil
	}
	if len(ops) != 1 {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("workspace-delta result expected one operation, got %d", len(ops))
	}
	state, err := operation.DecodeRemoteJobState(ops[0])
	if err != nil {
		return llm.ToolResult{CallID: callID}, err
	}
	switch ops[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		return textResult(callID, "Workspace-delta operation is still running."), nil
	case operation.StatusCanceled:
		return textResult(callID, "Workspace-delta operation was canceled."), nil
	}
	if state.TerminalError != "" {
		return textResult(callID, "Error: "+state.TerminalError), nil
	}
	return textResult(callID, state.TerminalResult), nil
}

// DeltaDefinition returns the model-visible WorkspaceDelta tool definition.
func DeltaDefinition() tool.Definition {
	return tool.Definition{Tool: llm.Tool{
		Type:        llm.ToolFunction,
		Name:        "WorkspaceDelta",
		Description: "After WriteFile/EditFile edits, call before finalizing to audit recorded changes. Default returns a bounded summary and latest cursor; cursor is an inclusive as-of sequence, not a changes-since cursor. Use diff=true with path or op_id for one bounded unified diff. Only WriteFile/EditFile are recorded; Bash and external edits are unobserved, so an empty result never means the workspace is clean.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cursor": map[string]any{"type": "string", "description": "Inclusive as-of sequence from a previous reply; omit for the latest full retained summary."},
				"diff":   map[string]any{"type": "boolean", "description": "Return a bounded unified diff instead of a summary."},
				"path":   map[string]any{"type": "string", "description": "Workspace-relative path; required with diff=true unless op_id is given."},
				"op_id":  map[string]any{"type": "string", "description": "Specific journal operation ID to diff."},
			},
			"additionalProperties": false,
		},
	}}
}
