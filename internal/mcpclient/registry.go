package mcpclient

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const (
	remotePlanType    operation.RemoteJobPlanType    = "pk.mcp.tool"
	remotePlanVersion operation.RemoteJobPlanVersion = 1
)

type registry struct {
	base        tool.Registry
	translators map[string]*translator
	definitions []tool.Definition
}

// DecorateRegistry adds namespaced MCP tools without shadowing existing tools.
// Conflicting MCP exports are omitted and returned as warnings.
func DecorateRegistry(base tool.Registry, host *Host) (tool.Registry, []error) {
	if base == nil {
		return nil, []error{errors.New("base tool registry is required")}
	}
	if host == nil {
		return base, nil
	}
	out := &registry{base: base, translators: make(map[string]*translator), definitions: append([]tool.Definition(nil), base.StaticDefinitions()...)}
	var issues []error
	for _, item := range host.Tools() {
		if _, exists := base.Resolve(item.Name); exists {
			issues = append(issues, fmt.Errorf("MCP tool %q conflicts with an existing tool; omitted", item.Name))
			continue
		}
		out.translators[item.Name] = &translator{name: item.Name}
		out.definitions = append(out.definitions, tool.Definition{Tool: llm.Tool{
			Type: llm.ToolFunction, Name: item.Name, Description: item.Description, Parameters: item.InputSchema,
		}})
	}
	if len(out.translators) == 0 {
		return base, issues
	}
	return out, issues
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
func (r *registry) RegisterSkill(skill tool.Skill) (tool.RegistrationID, error) {
	return r.base.RegisterSkill(skill)
}
func (r *registry) UnregisterSkill(id tool.RegistrationID) { r.base.UnregisterSkill(id) }
func (r *registry) Skills() []tool.Skill                   { return r.base.Skills() }

type translator struct{ name string }

type callPlan struct {
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
}

func (t *translator) Translate(callContext tool.Context, call llm.ToolCall) tool.CallStatus {
	if callContext == nil {
		return tool.CallStatus{Error: "MCP tool context is unavailable"}
	}
	args := json.RawMessage(call.Arguments)
	if len(args) == 0 || len(args) > maxToolResult || !json.Valid(args) {
		return tool.CallStatus{Error: "MCP tool arguments are invalid or exceed the size limit"}
	}
	var object map[string]any
	if err := json.Unmarshal(args, &object); err != nil || object == nil {
		return tool.CallStatus{Error: "MCP tool arguments must be a JSON object"}
	}
	data, err := json.Marshal(callPlan{Name: t.name, CallID: call.CallID, Arguments: args})
	if err != nil {
		return tool.CallStatus{Error: "encode MCP operation: " + err.Error()}
	}
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: remotePlanType, Version: remotePlanVersion, Data: jsontext.Value(data)})
	if err != nil {
		return tool.CallStatus{Error: "create MCP operation: " + err.Error()}
	}
	return tool.CallStatus{WaitingFor: []operation.ID{callContext.Submit(spec)}}
}

func (t *translator) TranslateResult(callID string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: status.Error}}}, nil
	}
	if len(operations) != 1 {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("MCP result expected one operation, got %d", len(operations))
	}
	state, err := operation.DecodeRemoteJobState(operations[0])
	if err != nil {
		return llm.ToolResult{CallID: callID}, err
	}
	switch operations[0].Status {
	case operation.StatusReady, operation.StatusAwaiting, operation.StatusCanceling:
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "MCP tool is running."}}}, nil
	case operation.StatusCompleted, operation.StatusFailed, operation.StatusCanceled:
	default:
		return llm.ToolResult{CallID: callID}, fmt.Errorf("MCP operation has unexpected status %q", operations[0].Status)
	}
	if operations[0].Status == operation.StatusCanceled {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "MCP tool was canceled."}}}, nil
	}
	if state.TerminalError != "" {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: state.TerminalError}}}, nil
	}
	var outputs []llm.ToolResultOutput
	if err := json.Unmarshal([]byte(state.TerminalResult), &outputs); err != nil {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("decode MCP tool result: %w", err)
	}
	if len(outputs) == 0 {
		outputs = []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "MCP tool returned no content."}}
	}
	return llm.ToolResult{CallID: callID, Output: outputs}, nil
}

// RemoteJobHandlers supplies a nonblocking executor for calls submitted by the
// registry. The returned handler stops in-flight MCP calls when ctx is canceled.
func (h *Host) RemoteJobHandlers(ctx context.Context) []operation.RemoteJobHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	return []operation.RemoteJobHandler{newRemoteHandler(ctx, h)}
}

type job struct {
	op       operation.Operation
	cancel   context.CancelFunc
	canceled bool
}

type remoteHandler struct {
	ctx     context.Context
	host    *Host
	mu      sync.Mutex
	jobs    map[operation.ID]*job
	updates chan operation.Operation
	wg      sync.WaitGroup
	done    chan struct{}
}

func newRemoteHandler(ctx context.Context, host *Host) *remoteHandler {
	h := &remoteHandler{ctx: ctx, host: host, jobs: make(map[operation.ID]*job), updates: make(chan operation.Operation, 64), done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		for _, item := range h.jobs {
			item.cancel()
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
		return errors.New("unsupported MCP operation plan")
	}
	var plan callPlan
	if err := json.Unmarshal(state.Plan.Data, &plan); err != nil {
		return fmt.Errorf("decode MCP operation: %w", err)
	}
	tool, ok := h.host.tool(plan.Name)
	if !ok || plan.CallID == "" || !json.Valid(plan.Arguments) || tool.Name != plan.Name {
		return errors.New("invalid or unavailable MCP operation payload")
	}
	var args map[string]any
	if err := json.Unmarshal(plan.Arguments, &args); err != nil || args == nil {
		return errors.New("MCP operation arguments must be a JSON object")
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
	item := &job{op: *awaiting.Operation, cancel: cancel}
	h.jobs[op.ID] = item
	h.wg.Add(1)
	h.mu.Unlock()
	go h.execute(jobCtx, op.ID, item, plan, args)
	return h.publish(*awaiting.Operation)
}

func (h *remoteHandler) CancelRemoteJob(id operation.ID, _ string) error {
	h.mu.Lock()
	if item := h.jobs[id]; item != nil {
		item.canceled = true
		item.cancel()
	}
	h.mu.Unlock()
	return nil
}

func (h *remoteHandler) execute(ctx context.Context, id operation.ID, item *job, plan callPlan, args map[string]any) {
	defer h.wg.Done()
	result, runErr := h.host.call(ctx, plan.Name, args)
	h.mu.Lock()
	canceled := item.canceled || ctx.Err() != nil
	delete(h.jobs, id)
	h.mu.Unlock()
	var step operation.Step
	var err error
	if canceled {
		step, err = operation.CancelRemoteJob(item.op)
	} else if runErr != nil {
		safeErr := h.host.redactForTool(plan.Name, runErr.Error())
		step, err = operation.FailRemoteJob(item.op, errors.New(safeErr))
	} else {
		outputs := contentToOutputs(result)
		outputs = h.host.redactOutputsForTool(plan.Name, outputs)
		encoded, marshalErr := json.Marshal(outputs)
		if marshalErr != nil {
			step, err = operation.FailRemoteJob(item.op, marshalErr)
		} else {
			state, decodeErr := operation.DecodeRemoteJobState(item.op)
			if decodeErr != nil {
				step, err = operation.FailRemoteJob(item.op, decodeErr)
			} else {
				state.TerminalResult = string(encoded)
				step, err = operation.UpdateRemoteJob(item.op, state, operation.StatusCompleted)
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

func contentToOutputs(result *mcp.CallToolResult) []llm.ToolResultOutput {
	if result == nil {
		return []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "MCP server returned an empty result."}}
	}
	outputs := make([]llm.ToolResultOutput, 0, len(result.Content)+1)
	remaining := maxToolResult
	const omitted = " [additional MCP content omitted at output limit]"
	for index, content := range result.Content {
		var text string
		switch item := content.(type) {
		case *mcp.TextContent:
			text = item.Text
		default:
			text = fmt.Sprintf("[MCP returned unsupported %T content; content was not delivered]", content)
		}
		if remaining <= 0 {
			break
		}
		if len(text) > remaining || (len(text) == remaining && index < len(result.Content)-1) {
			marker := " [output truncated]"
			if len(text) <= remaining && index < len(result.Content)-1 {
				marker = omitted
			}
			prefixLimit := remaining - len(marker)
			if prefixLimit < 0 {
				prefixLimit = 0
			}
			text = text[:utf8Prefix(text, prefixLimit)] + marker
			remaining = 0
			outputs = append(outputs, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: text})
			break
		} else {
			remaining -= len(text)
		}
		outputs = append(outputs, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: text})
	}
	if len(outputs) == 0 {
		outputs = append(outputs, llm.ToolResultOutput{Kind: llm.ToolResultText, Value: "MCP tool returned no text content."})
	}
	if result.IsError && len(outputs) != 0 {
		outputs[0].Value = "MCP tool reported an error: " + outputs[0].Value
	}
	return outputs
}

func utf8Prefix(text string, limit int) int {
	if limit >= len(text) {
		return len(text)
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return limit
}
