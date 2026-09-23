package experiment

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

var subagentToolNames = map[string]struct{}{
	"SubagentStart": {}, "SubagentStatus": {}, "SubagentSend": {}, "SubagentWait": {}, "SubagentCancel": {},
}

const subagentDispatcherDescription = "Manage child agents in the shared writable workspace (2 active, 8 queued). Start requires task and task_only; start tasks are limited to 16 KiB. Status, wait, and cancel need child_id; send needs child_id and text. Code tasks declare 1–64 unique workspace-relative files; task_only=true omits file ownership. Ownership is coordination only: children retain the parent's tools, write access, and OS permissions, not a sandbox. Coordinate before editing unclaimed files. Children cannot spawn children. Optional model and effort override configured child defaults."

// SubagentDispatcherMetrics measures the serialized llm.Tool bytes removed by
// the benchmark-only action dispatcher.
type SubagentDispatcherMetrics struct {
	BeforeBytes int64 `json:"subagent_tool_bytes_before"`
	AfterBytes  int64 `json:"subagent_tool_bytes_after"`
}

// CompactSubagentTools replaces the five model-visible helperregistry tools
// with one flat action-dispatch tool. It is intended only for controlled
// benchmark arms; it does not change the production registry.
func CompactSubagentTools(base tool.Registry) (tool.Registry, SubagentDispatcherMetrics, error) {
	if base == nil {
		return nil, SubagentDispatcherMetrics{}, errors.New("subagent registry is required")
	}
	if _, exists := base.Resolve("Subagent"); exists {
		return nil, SubagentDispatcherMetrics{}, errors.New("cannot compact subagent tools: Subagent name is already registered")
	}
	definitionNames := map[string]bool{}
	var before int64
	for _, definition := range base.StaticDefinitions() {
		name := definition.Tool.Name
		if _, isSubagent := subagentToolNames[name]; isSubagent {
			if _, ok := base.Resolve(name); !ok {
				return nil, SubagentDispatcherMetrics{}, fmt.Errorf("subagent tool %s has no translator", name)
			}
			definitionNames[name] = true
			encoded, err := json.Marshal(definition.Tool)
			if err != nil {
				return nil, SubagentDispatcherMetrics{}, errors.New("measure current subagent tool schema")
			}
			before += int64(len(encoded))
		}
	}
	if len(definitionNames) != len(subagentToolNames) {
		return nil, SubagentDispatcherMetrics{}, fmt.Errorf("expected five subagent tools, found %d", len(definitionNames))
	}
	definition := dispatcherDefinition()
	encoded, err := json.Marshal(definition.Tool)
	if err != nil {
		return nil, SubagentDispatcherMetrics{}, errors.New("measure compact subagent tool schema")
	}
	resultTranslator, ok := base.Resolve("SubagentStatus")
	if !ok {
		return nil, SubagentDispatcherMetrics{}, errors.New("subagent status result translator is unavailable")
	}
	return &subagentDispatcherRegistry{Registry: base, definition: definition, translator: &subagentDispatcher{base: base, resultTranslator: resultTranslator}}, SubagentDispatcherMetrics{
		BeforeBytes: before,
		AfterBytes:  int64(len(encoded)),
	}, nil
}

func dispatcherDefinition() tool.Definition {
	return tool.Definition{Tool: llm.Tool{
		Type:        llm.ToolFunction,
		Name:        "Subagent",
		Description: subagentDispatcherDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"start", "status", "send", "wait", "cancel"}, "description": "start: task, task_only, optional model/effort; status/wait/cancel: child_id; send: child_id, text."},
				"task":      map[string]any{"type": "string"},
				"files":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 64, "description": "For start: 1–64 unique workspace-relative files; omit when task_only=true."},
				"task_only": map[string]any{"type": "boolean", "description": "Required for start; true omits ownership, false means files are declared."},
				"child_id":  map[string]any{"type": "string"},
				"text":      map[string]any{"type": "string"},
				"model":     map[string]any{"type": "string"},
				"effort":    map[string]any{"type": "string"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}}
}

type subagentDispatcherRegistry struct {
	tool.Registry
	definition tool.Definition
	translator tool.Translator
}

func (r *subagentDispatcherRegistry) StaticDefinitions() []tool.Definition {
	definitions := r.Registry.StaticDefinitions()
	result := make([]tool.Definition, 0, len(definitions)-len(subagentToolNames)+1)
	inserted := false
	for _, definition := range definitions {
		if _, compacted := subagentToolNames[definition.Tool.Name]; compacted {
			if !inserted {
				result = append(result, r.definition)
				inserted = true
			}
			continue
		}
		result = append(result, definition)
	}
	if !inserted {
		result = append(result, r.definition)
	}
	return result
}

func (r *subagentDispatcherRegistry) Resolve(name string) (tool.Translator, bool) {
	if name == "Subagent" {
		return r.translator, true
	}
	return r.Registry.Resolve(name)
}

type subagentDispatcher struct {
	base             tool.Registry
	resultTranslator tool.Translator
}

type dispatcherRequest struct {
	Action   string    `json:"action"`
	Task     *string   `json:"task"`
	Files    *[]string `json:"files"`
	TaskOnly *bool     `json:"task_only"`
	ChildID  *string   `json:"child_id"`
	Text     *string   `json:"text"`
	Model    *string   `json:"model"`
	Effort   *string   `json:"effort"`
}

func (t *subagentDispatcher) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	if len(call.Arguments) > 16<<10 {
		return tool.CallStatus{Error: "subagent tool arguments exceed 16 KiB"}
	}
	decoder := json.NewDecoder(strings.NewReader(call.Arguments))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return tool.CallStatus{Error: "invalid subagent tool arguments: " + err.Error()}
	}
	if raw == nil {
		return tool.CallStatus{Error: "invalid subagent tool arguments: expected an object"}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return tool.CallStatus{Error: "invalid subagent tool arguments: expected one JSON object"}
	}
	for key, value := range raw {
		switch key {
		case "action", "task", "files", "task_only", "child_id", "text", "model", "effort":
		default:
			return tool.CallStatus{Error: "invalid subagent tool arguments: unknown field " + key}
		}
		if strings.TrimSpace(string(value)) == "null" {
			return tool.CallStatus{Error: "invalid subagent tool arguments: field " + key + " must not be null"}
		}
	}
	var req dispatcherRequest
	if err := json.Unmarshal([]byte(call.Arguments), &req); err != nil {
		return tool.CallStatus{Error: "invalid subagent tool arguments: " + err.Error()}
	}
	legacyName, normalized, err := normalizeDispatcherRequest(req)
	if err != nil {
		return tool.CallStatus{Error: err.Error()}
	}
	delegate, ok := t.base.Resolve(legacyName)
	if !ok {
		return tool.CallStatus{Error: "subagent action is unavailable"}
	}
	call.Name = legacyName
	call.Arguments = normalized
	status := delegate.Translate(ctx, call)
	return status
}

func (t *subagentDispatcher) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	if t.resultTranslator == nil {
		return llm.ToolResult{CallID: callID}, errors.New("subagent result translator is unavailable")
	}
	return t.resultTranslator.TranslateResult(callID, status, ops)
}

func normalizeDispatcherRequest(req dispatcherRequest) (string, string, error) {
	allowed := map[string]bool{}
	legacy := ""
	switch req.Action {
	case "start":
		legacy = "SubagentStart"
		allowed = map[string]bool{"task": true, "files": true, "task_only": true, "model": true, "effort": true}
		if req.Task == nil || req.TaskOnly == nil {
			return "", "", errors.New("start requires task and task_only")
		}
		if strings.TrimSpace(*req.Task) == "" || len(*req.Task) > 16<<10 {
			return "", "", errors.New("start task must be non-empty and at most 16 KiB")
		}
	case "status", "wait", "cancel":
		legacy = map[string]string{"status": "SubagentStatus", "wait": "SubagentWait", "cancel": "SubagentCancel"}[req.Action]
		allowed = map[string]bool{"child_id": true}
		if req.ChildID == nil || strings.TrimSpace(*req.ChildID) == "" {
			return "", "", errors.New(req.Action + " requires child_id")
		}
	case "send":
		legacy = "SubagentSend"
		allowed = map[string]bool{"child_id": true, "text": true}
		if req.ChildID == nil || strings.TrimSpace(*req.ChildID) == "" || req.Text == nil || strings.TrimSpace(*req.Text) == "" {
			return "", "", errors.New("send requires child_id and non-empty text")
		}
		if len(*req.Text) > 16<<10 {
			return "", "", errors.New("send text exceeds 16 KiB")
		}
	default:
		return "", "", errors.New("action must be start, status, send, wait, or cancel")
	}
	for field, present := range map[string]bool{
		"task": req.Task != nil, "files": req.Files != nil, "task_only": req.TaskOnly != nil,
		"child_id": req.ChildID != nil, "text": req.Text != nil, "model": req.Model != nil, "effort": req.Effort != nil,
	} {
		if present && !allowed[field] {
			return "", "", fmt.Errorf("%s does not accept %s", req.Action, field)
		}
	}
	payload := map[string]any{}
	if req.Task != nil {
		payload["task"] = *req.Task
	}
	if req.Files != nil {
		payload["files"] = *req.Files
	}
	if req.TaskOnly != nil {
		payload["task_only"] = *req.TaskOnly
	}
	if req.ChildID != nil {
		payload["child_id"] = *req.ChildID
	}
	if req.Text != nil {
		payload["text"] = *req.Text
	}
	if req.Model != nil {
		payload["model"] = *req.Model
	}
	if req.Effort != nil {
		payload["effort"] = *req.Effort
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", "", errors.New("encode subagent action arguments")
	}
	return legacy, string(encoded), nil
}
