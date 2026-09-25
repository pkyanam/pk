package goals

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func Decorator(store *Store, sessionID, generationID, turnID string) func(tool.Registry) tool.Registry {
	return func(base tool.Registry) tool.Registry {
		if base == nil || store == nil || sessionID == "" {
			return base
		}
		return &goalRegistry{base: base, store: store, sessionID: sessionID, generationID: generationID, turnID: turnID}
	}
}

type goalRegistry struct {
	base                            tool.Registry
	store                           *Store
	sessionID, generationID, turnID string
}

func (r *goalRegistry) StaticDefinitions() []tool.Definition {
	defs := r.base.StaticDefinitions()
	defs = append(defs,
		tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: "GoalComplete", Description: "Mark the active goal complete only after verifying every stated success criterion. Include concrete test, file, or command evidence. Never use this to pause or to claim unverified work.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"evidence": map[string]any{"type": "string", "minLength": 16}}, "required": []string{"evidence"}, "additionalProperties": false}}},
		tool.Definition{Tool: llm.Tool{Type: llm.ToolFunction, Name: "GoalBlocker", Description: "Report the specific blocker preventing the active goal. Use only after trying a concrete next step. The same blocker must be reported in three separate goal turns before pk marks the goal blocked.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"blocker": map[string]any{"type": "string", "minLength": 8, "maxLength": 1000}}, "required": []string{"blocker"}, "additionalProperties": false}}},
	)
	return defs
}
func (r *goalRegistry) Resolve(name string) (tool.Translator, bool) {
	if name == "GoalComplete" || name == "GoalBlocker" {
		return goalTranslator{store: r.store, sessionID: r.sessionID, generationID: r.generationID, turnID: r.turnID, name: name}, true
	}
	return r.base.Resolve(name)
}
func (r *goalRegistry) RegisterSkill(s tool.Skill) (tool.RegistrationID, error) {
	return r.base.RegisterSkill(s)
}
func (r *goalRegistry) UnregisterSkill(id tool.RegistrationID) { r.base.UnregisterSkill(id) }
func (r *goalRegistry) Skills() []tool.Skill                   { return r.base.Skills() }

type goalTranslator struct {
	store                                 *Store
	sessionID, generationID, turnID, name string
}

func (t goalTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	if ctx == nil {
		return tool.CallStatus{Error: "goal tool context unavailable"}
	}
	var args struct {
		Evidence     string `json:"evidence"`
		Blocker      string `json:"blocker"`
		GenerationID string `json:"generation_id"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return tool.CallStatus{Error: "invalid goal tool arguments: " + err.Error()}
	}
	if t.generationID == "" {
		return tool.CallStatus{Error: "goal generation is unavailable; do not report completion or blocker"}
	}
	if t.name == "GoalComplete" && len(args.Evidence) < 16 {
		return tool.CallStatus{Error: "completion requires concrete verification evidence"}
	}
	if t.name == "GoalBlocker" && (t.turnID == "" || len(args.Blocker) < 8 || len(args.Blocker) > 1000) {
		return tool.CallStatus{Error: "a goal turn ID and concise blocker reason are required"}
	}
	args.GenerationID = t.generationID
	payload, err := json.Marshal(args)
	if err != nil {
		return tool.CallStatus{Error: "encode goal tool operation: " + err.Error()}
	}
	spec, err := operation.NewValueSpec(jsontext.Value(payload))
	if err != nil {
		return tool.CallStatus{Error: "create goal tool operation: " + err.Error()}
	}
	return tool.CallStatus{WaitingFor: []operation.ID{ctx.Submit(spec)}}
}
func (t goalTranslator) TranslateResult(callID string, status tool.CallStatus, ops []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: status.Error}}}, nil
	}
	if len(ops) != 1 || len(status.WaitingFor) != 1 || ops[0].ID != status.WaitingFor[0] {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("%s expected one goal operation, got %d", t.name, len(ops))
	}
	op := ops[0]
	if op.Status != operation.StatusCompleted {
		if op.Status == operation.StatusFailed || op.Status == operation.StatusCanceled {
			return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Goal operation did not complete."}}}, nil
		}
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "Goal operation is running."}}}, nil
	}
	value, err := operation.DecodeValue(op)
	if err != nil {
		return llm.ToolResult{CallID: callID}, err
	}
	var args struct {
		Evidence     string `json:"evidence"`
		Blocker      string `json:"blocker"`
		GenerationID string `json:"generation_id"`
	}
	if err := json.Unmarshal(value, &args); err != nil {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("decode goal operation: %w", err)
	}
	if args.GenerationID == "" || args.GenerationID != t.generationID {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: "This goal operation is stale; the saved goal has changed."}}}, nil
	}
	var result string
	if t.name == "GoalComplete" {
		_, err = t.store.CompleteGeneration(context.Background(), t.sessionID, args.GenerationID, args.Evidence)
		result = "Goal marked complete after verification."
	} else {
		_, err = t.store.ReportBlockerGeneration(context.Background(), t.sessionID, args.GenerationID, t.turnID, args.Blocker)
		result = "Goal blocker recorded for this turn."
	}
	if err != nil {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: err.Error()}}}, nil
	}
	return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: result}}}, nil
}
