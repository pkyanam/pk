// Package helperregistry exposes parent-only tools for managing child agents.
package helperregistry

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"sync"

	"github.com/pkyanam/pk/internal/subagents"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const (
	planType    operation.RemoteJobPlanType    = "pk.subagent.tool"
	planVersion operation.RemoteJobPlanVersion = 1
)

var toolNames = []string{"SubagentStart", "SubagentStatus", "SubagentSend", "SubagentWait", "SubagentCancel"}

type registry struct {
	base        tool.Registry
	translators map[string]*translator
	definitions []tool.Definition
}

// DecorateRegistry adds parent-facing subagent management tools. Child runners
// must not use this decorator; Manager also rejects launches at depth > 0.
func DecorateRegistry(base tool.Registry, manager *subagents.Manager) tool.Registry {
	if base == nil || manager == nil {
		return base
	}
	r := &registry{base: base, translators: map[string]*translator{}, definitions: append([]tool.Definition(nil), base.StaticDefinitions()...)}
	for _, name := range toolNames {
		if _, exists := base.Resolve(name); exists {
			continue
		}
		r.translators[name] = &translator{name: name}
		r.definitions = append(r.definitions, tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: name, Description: description(name), Parameters: schema(name)}})
	}
	if len(r.translators) == 0 {
		return base
	}
	return r
}
func (r *registry) StaticDefinitions() []tool.Definition {
	return append([]tool.Definition(nil), r.definitions...)
}
func (r *registry) Resolve(name string) (tool.Translator, bool) {
	if t, ok := r.translators[name]; ok {
		return t, true
	}
	return r.base.Resolve(name)
}
func (r *registry) RegisterSkill(s tool.Skill) (tool.RegistrationID, error) {
	return r.base.RegisterSkill(s)
}
func (r *registry) UnregisterSkill(id tool.RegistrationID) { r.base.UnregisterSkill(id) }
func (r *registry) Skills() []tool.Skill                   { return r.base.Skills() }

type request struct {
	Action   string   `json:"action"`
	Task     string   `json:"task,omitempty"`
	Files    []string `json:"files,omitempty"`
	TaskOnly bool     `json:"task_only,omitempty"`
	ChildID  string   `json:"child_id,omitempty"`
	Text     string   `json:"text,omitempty"`
	Model    string   `json:"model,omitempty"`
	Effort   string   `json:"effort,omitempty"`
}
type jobPlan struct {
	RequestID string  `json:"request_id"`
	Request   request `json:"request"`
}
type translator struct{ name string }

func (t *translator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	if ctx == nil {
		return tool.CallStatus{Error: "subagent tool context unavailable"}
	}
	var req request
	if err := json.Unmarshal([]byte(call.Arguments), &req); err != nil {
		return tool.CallStatus{Error: "invalid subagent tool arguments: " + err.Error()}
	}
	req.Action = map[string]string{"SubagentStart": "start", "SubagentStatus": "status", "SubagentSend": "send", "SubagentWait": "wait", "SubagentCancel": "cancel"}[t.name]
	if len(call.Arguments) > 16<<10 {
		return tool.CallStatus{Error: "subagent tool arguments exceed 16 KiB"}
	}
	data, err := json.Marshal(jobPlan{RequestID: call.CallID, Request: req})
	if err != nil {
		return tool.CallStatus{Error: err.Error()}
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: planType, Version: planVersion, Data: jsontext.Value(data)})
	if err != nil {
		return tool.CallStatus{Error: err.Error()}
	}
	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}
func (t *translator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		return textResult(callID, status.Error), nil
	}
	if len(ops) != 1 {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("subagent result expected one operation, got %d", len(ops))
	}
	state, err := operation.DecodeRemoteJobState(ops[0])
	if err != nil {
		return llm.ToolResult{CallID: callID}, err
	}
	switch ops[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		return textResult(callID, "Subagent operation is running."), nil
	case operation.StatusCompleted, operation.StatusFailed, operation.StatusCanceled:
	default:
		return llm.ToolResult{CallID: callID}, fmt.Errorf("unexpected subagent operation status %q", ops[0].Status)
	}
	if ops[0].Status == operation.StatusCanceled {
		return textResult(callID, "Subagent operation canceled."), nil
	}
	if state.TerminalError != "" {
		return textResult(callID, state.TerminalError), nil
	}
	return textResult(callID, state.TerminalResult), nil
}
func textResult(id, text string) llm.ToolResult {
	return llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}
}

type job struct {
	op       operation.Operation
	cancel   context.CancelFunc
	canceled bool
}
type remoteHandler struct {
	ctx     context.Context
	manager *subagents.Manager
	mu      sync.Mutex
	jobs    map[operation.ID]*job
	updates chan operation.Operation
	wg      sync.WaitGroup
	done    chan struct{}
}

func RemoteJobHandlers(ctx context.Context, m *subagents.Manager) []operation.RemoteJobHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		return nil
	}
	h := &remoteHandler{ctx: ctx, manager: m, jobs: map[operation.ID]*job{}, updates: make(chan operation.Operation, 64), done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		for _, j := range h.jobs {
			j.cancel()
		}
		h.mu.Unlock()
		h.wg.Wait()
		close(h.updates)
		close(h.done)
	}()
	return []operation.RemoteJobHandler{h}
}
func (*remoteHandler) RemoteJobPlanType() operation.RemoteJobPlanType       { return planType }
func (*remoteHandler) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return planVersion }
func (h *remoteHandler) RemoteJobUpdates() <-chan operation.Operation       { return h.updates }
func (h *remoteHandler) Wait()                                              { <-h.done }
func (h *remoteHandler) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err
	}
	if state.Plan.Type != planType || state.Plan.Version != planVersion {
		return errors.New("unsupported subagent operation")
	}
	var p jobPlan
	if err = json.Unmarshal(state.Plan.Data, &p); err != nil {
		return err
	}
	if p.RequestID == "" {
		return errors.New("subagent request id is required")
	}
	updated, err := operation.UpdateRemoteJob(op, state, operation.StatusAwaiting)
	if err != nil {
		return err
	}
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		return h.ctx.Err()
	}
	if _, ok := h.jobs[op.ID]; ok {
		h.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(h.ctx)
	j := &job{op: *updated.Operation, cancel: cancel}
	h.jobs[op.ID] = j
	h.wg.Add(1)
	h.mu.Unlock()
	go h.execute(ctx, op.ID, j, p)
	return h.publish(*updated.Operation)
}
func (h *remoteHandler) CancelRemoteJob(id operation.ID, _ string) error {
	h.mu.Lock()
	if j := h.jobs[id]; j != nil {
		j.canceled = true
		j.cancel()
	}
	h.mu.Unlock()
	return nil
}
func (h *remoteHandler) execute(ctx context.Context, id operation.ID, j *job, p jobPlan) {
	defer h.wg.Done()
	value, runErr := h.invoke(ctx, p.RequestID, p.Request)
	h.mu.Lock()
	canceled := j.canceled || ctx.Err() != nil
	delete(h.jobs, id)
	h.mu.Unlock()
	var step operation.Step
	var err error
	if canceled {
		step, err = operation.CancelRemoteJob(j.op)
	} else if runErr != nil {
		step, err = operation.FailRemoteJob(j.op, runErr)
	} else {
		state, e := operation.DecodeRemoteJobState(j.op)
		if e != nil {
			err = e
		} else {
			state.TerminalResult = value
			step, err = operation.UpdateRemoteJob(j.op, state, operation.StatusCompleted)
		}
	}
	if err == nil && step.Operation != nil {
		_ = h.publish(*step.Operation)
	}
}
func (h *remoteHandler) invoke(ctx context.Context, requestID string, r request) (string, error) {
	var value any
	switch r.Action {
	case "start":
		c, err := h.manager.Launch(ctx, subagents.LaunchRequest{Task: r.Task, Files: r.Files, TaskOnly: r.TaskOnly, Model: r.Model, Effort: r.Effort, RequestID: requestID})
		if err != nil {
			return "", err
		}
		value = c
	case "status":
		c, ok := h.manager.Status(r.ChildID)
		if !ok {
			return "", errors.New("unknown subagent")
		}
		value = c
	case "send":
		if err := h.manager.SendInput(ctx, r.ChildID, r.Text); err != nil {
			return "", err
		}
		value = map[string]any{"accepted": true, "child_id": r.ChildID}
	case "wait":
		report, err := h.manager.Wait(ctx, r.ChildID)
		if err != nil {
			return "", err
		}
		value = report
	case "cancel":
		if err := h.manager.Cancel(r.ChildID); err != nil {
			return "", err
		}
		value = map[string]any{"canceled": true, "child_id": r.ChildID}
	default:
		return "", errors.New("unknown subagent action")
	}
	b, err := json.Marshal(value)
	return string(b), err
}
func (h *remoteHandler) publish(op operation.Operation) error {
	select {
	case h.updates <- op:
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}
func description(name string) string {
	switch name {
	case "SubagentStart":
		return "Start a child coding agent in the shared writable workspace (default max 2 active; up to 8 queued). Set task_only=true and omit files for an investigation with no exclusive file ownership. Otherwise declare 1–64 unique workspace-relative files it owns. Ownership is coordination guidance only: the child has the same tools, write access, and OS permissions, not a sandbox or read-only restriction. Coordinate before changing files outside owned paths. Task is limited to 16 KiB; child agents cannot recursively spawn agents."
	case "SubagentStatus":
		return "Get the state of a child coding agent."
	case "SubagentSend":
		return "Queue text steering for a running child agent; accepted after durable persistence."
	case "SubagentWait":
		return "Wait for a child agent to finish its current task and return its report."
	default:
		return "Cancel a child coding agent."
	}
}
func schema(name string) map[string]any {
	props := map[string]any{}
	required := []string{}
	switch name {
	case "SubagentStart":
		props["task"] = map[string]any{"type": "string"}
		props["files"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 64, "description": "Optional when task_only is true; otherwise declare 1–64 unique workspace-relative files for coordination ownership."}
		props["task_only"] = map[string]any{"type": "boolean", "description": "Required explicit no-file-ownership mode. This does not remove workspace write access or enforce a sandbox."}
		props["model"] = map[string]any{"type": "string"}
		props["effort"] = map[string]any{"type": "string"}
		required = []string{"task", "task_only"}
	case "SubagentSend":
		props["child_id"] = map[string]any{"type": "string"}
		props["text"] = map[string]any{"type": "string"}
		required = []string{"child_id", "text"}
	default:
		props["child_id"] = map[string]any{"type": "string"}
		required = []string{"child_id"}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
