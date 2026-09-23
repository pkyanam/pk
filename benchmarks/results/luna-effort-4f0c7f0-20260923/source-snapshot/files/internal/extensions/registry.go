package extensions

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"sync"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const (
	remotePlanType    operation.RemoteJobPlanType    = "pk.extension.tool"
	remotePlanVersion operation.RemoteJobPlanVersion = 1
)

type registry struct {
	base        tool.Registry
	tools       map[string]*extensionTranslator
	definitions []tool.Definition
}

// DecorateRegistry adds non-conflicting extension tools. Existing registry
// names always win; an extension with a collision is omitted as a unit.
func DecorateRegistry(base tool.Registry, host *Host) (tool.Registry, []error) {
	if base == nil {
		return nil, []error{errors.New("base tool registry is required")}
	}
	if host == nil {
		return base, nil
	}
	out := &registry{base: base, tools: make(map[string]*extensionTranslator), definitions: append([]tool.Definition(nil), base.StaticDefinitions()...)}
	var issues []error
	conflicted := make(map[string]bool)
	for _, spec := range host.Tools() {
		if _, exists := base.Resolve(spec.Name); exists {
			owner := host.ownerOfTool(spec.Name)
			if owner != "" {
				conflicted[owner] = true
			}
			issue := fmt.Errorf("extension tool %q conflicts with a built-in tool; extension disabled", spec.Name)
			issues = append(issues, issue)
			host.rejectToolName(spec.Name, issue)
		}
	}
	for _, spec := range host.Tools() {
		owner := host.ownerOfTool(spec.Name)
		if conflicted[owner] {
			continue
		}
		var parameters map[string]any
		if err := json.Unmarshal(spec.Parameters, &parameters); err != nil {
			issues = append(issues, fmt.Errorf("extension tool %q has invalid parameters: %w", spec.Name, err))
			continue
		}
		out.tools[spec.Name] = &extensionTranslator{name: spec.Name}
		out.definitions = append(out.definitions, tool.Definition{Tool: llm.Tool{
			Type: llm.ToolFunction, Name: spec.Name, Description: spec.Description, Parameters: parameters,
		}})
	}
	if len(out.tools) == 0 {
		return base, issues
	}
	return out, issues
}

func (r *registry) StaticDefinitions() []tool.Definition {
	return append([]tool.Definition(nil), r.definitions...)
}

func (r *registry) Resolve(name string) (tool.Translator, bool) {
	if translator, ok := r.tools[name]; ok {
		return translator, true
	}
	return r.base.Resolve(name)
}

func (r *registry) RegisterSkill(skill tool.Skill) (tool.RegistrationID, error) {
	return r.base.RegisterSkill(skill)
}
func (r *registry) UnregisterSkill(id tool.RegistrationID) { r.base.UnregisterSkill(id) }
func (r *registry) Skills() []tool.Skill                   { return r.base.Skills() }

type extensionTranslator struct{ name string }

type extensionPlan struct {
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
}

func (t *extensionTranslator) Translate(callContext tool.Context, call llm.ToolCall) tool.CallStatus {
	if callContext == nil {
		return tool.CallStatus{Error: "extension tool context is unavailable"}
	}
	if len(call.Arguments) > maxMessageSize {
		return tool.CallStatus{Error: "extension tool arguments exceed protocol message limit"}
	}
	args := json.RawMessage(call.Arguments)
	if !json.Valid(args) {
		return tool.CallStatus{Error: "extension tool arguments must be valid JSON"}
	}
	data, err := json.Marshal(extensionPlan{Name: t.name, CallID: call.CallID, Arguments: args})
	if err != nil {
		return tool.CallStatus{Error: "encode extension tool request: " + err.Error()}
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: remotePlanType, Version: remotePlanVersion, Data: jsontext.Value(data)})
	if err != nil {
		return tool.CallStatus{Error: "create extension operation: " + err.Error()}
	}
	return tool.CallStatus{WaitingFor: []operation.ID{callContext.Submit(spec)}}
}

func (t *extensionTranslator) TranslateResult(callID string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: status.Error}}}, nil
	}
	if len(operations) != 1 {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("extension result expected one terminal operation, got %d", len(operations))
	}
	state, err := operation.DecodeRemoteJobState(operations[0])
	if err != nil {
		return llm.ToolResult{CallID: callID}, err
	}
	switch operations[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Extension tool is running."}}}, nil
	case operation.StatusCompleted, operation.StatusFailed, operation.StatusCanceled:
	default:
		return llm.ToolResult{CallID: callID}, fmt.Errorf("extension operation has unexpected status %q", operations[0].Status)
	}
	text := state.TerminalResult
	if state.TerminalError != "" {
		text = state.TerminalError
	}
	if operations[0].Status == operation.StatusCanceled {
		text = "Extension tool was canceled."
	}
	if text == "" {
		text = "Extension tool returned no content."
	}
	var result ToolResult
	if state.TerminalError == "" && operations[0].Status != operation.StatusCanceled {
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			return llm.ToolResult{CallID: callID}, fmt.Errorf("decode extension result for %s: %w (result %q)", operations[0].Status, err, text)
		}
	}
	if result.Error != "" {
		text = result.Error
	}
	outputs := make([]llm.ToolResultOutput, 0, len(result.Content))
	for _, item := range result.Content {
		outputs = append(outputs, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: item.Text})
	}
	if len(outputs) == 0 {
		outputs = append(outputs, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: text})
	}
	return llm.ToolResult{CallID: callID, Output: outputs}, nil
}

// RemoteJobHandlers returns the run-scoped async executor. It should be wired
// to runner.Options.RemoteJobHandlers so extension work never blocks Translate.
func (h *Host) RemoteJobHandlers(ctx context.Context) []operation.RemoteJobHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	return []operation.RemoteJobHandler{newRemoteHandler(ctx, h)}
}

type extensionJob struct {
	op       operation.Operation
	cancel   context.CancelFunc
	canceled bool
}

type remoteHandler struct {
	ctx     context.Context
	host    *Host
	mu      sync.Mutex
	jobs    map[operation.ID]*extensionJob
	updates chan operation.Operation
	wg      sync.WaitGroup
	done    chan struct{}
}

func newRemoteHandler(ctx context.Context, host *Host) *remoteHandler {
	h := &remoteHandler{ctx: ctx, host: host, jobs: make(map[operation.ID]*extensionJob), updates: make(chan operation.Operation, 64), done: make(chan struct{})}
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

func (*remoteHandler) RemoteJobPlanType() operation.RemoteJobPlanType       { return remotePlanType }
func (*remoteHandler) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return remotePlanVersion }
func (h *remoteHandler) RemoteJobUpdates() <-chan operation.Operation       { return h.updates }
func (h *remoteHandler) Wait()                                              { <-h.done }

func (h *remoteHandler) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err
	}
	if state.Plan.Type != remotePlanType || state.Plan.Version != remotePlanVersion {
		return errors.New("unsupported extension operation plan")
	}
	var plan extensionPlan
	if err := json.Unmarshal(state.Plan.Data, &plan); err != nil {
		return fmt.Errorf("decode extension operation: %w", err)
	}
	if plan.Name == "" || plan.CallID == "" || !json.Valid(plan.Arguments) {
		return errors.New("invalid extension operation payload")
	}
	awaiting, err := operation.UpdateRemoteJob(op, state, operation.StatusAwaiting)
	if err != nil {
		return err
	}
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
	job := &extensionJob{op: *awaiting.Operation, cancel: cancel}
	h.jobs[op.ID] = job
	h.wg.Add(1)
	h.mu.Unlock()
	go h.execute(jobCtx, op.ID, job, plan)
	return h.publish(*awaiting.Operation)
}

func (h *remoteHandler) CancelRemoteJob(id operation.ID, _ string) error {
	h.mu.Lock()
	job := h.jobs[id]
	if job != nil {
		job.canceled = true
		job.cancel()
	}
	h.mu.Unlock()
	return nil
}

func (h *remoteHandler) execute(ctx context.Context, id operation.ID, job *extensionJob, plan extensionPlan) {
	defer h.wg.Done()
	result, runErr := h.host.ExecuteTool(ctx, plan.Name, plan.CallID, plan.Arguments)
	h.mu.Lock()
	canceled := job.canceled || ctx.Err() != nil
	delete(h.jobs, id)
	h.mu.Unlock()
	var step operation.Step
	var err error
	if canceled {
		step, err = operation.CancelRemoteJob(job.op)
	} else if runErr != nil {
		step, err = operation.FailRemoteJob(job.op, runErr)
	} else {
		payload, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			step, err = operation.FailRemoteJob(job.op, marshalErr)
		} else {
			state, decodeErr := operation.DecodeRemoteJobState(job.op)
			if decodeErr != nil {
				step, err = operation.FailRemoteJob(job.op, decodeErr)
			} else {
				state.TerminalResult = string(payload)
				step, err = operation.UpdateRemoteJob(job.op, state, operation.StatusCompleted)
			}
		}
	}
	if err == nil && step.Operation != nil {
		_ = h.publish(*step.Operation)
	}
}

func (h *remoteHandler) publish(op operation.Operation) error {
	select {
	case h.updates <- op:
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}
