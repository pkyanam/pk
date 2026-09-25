package goals

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func Decorator(store *Store, sessionID, turnID string) func(tool.Registry) tool.Registry {
	return func(base tool.Registry) tool.Registry {
		if base == nil || store == nil || sessionID == "" {
			return base
		}
		return &goalRegistry{base: base, store: store, sessionID: sessionID, turnID: turnID}
	}
}

type goalRegistry struct {
	base              tool.Registry
	store             *Store
	sessionID, turnID string
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
		return goalTranslator{store: r.store, sessionID: r.sessionID, turnID: r.turnID, name: name}, true
	}
	return r.base.Resolve(name)
}
func (r *goalRegistry) RegisterSkill(s tool.Skill) (tool.RegistrationID, error) {
	return r.base.RegisterSkill(s)
}
func (r *goalRegistry) UnregisterSkill(id tool.RegistrationID) { r.base.UnregisterSkill(id) }
func (r *goalRegistry) Skills() []tool.Skill                   { return r.base.Skills() }

type goalTranslator struct {
	store                   *Store
	sessionID, turnID, name string
}

func (t goalTranslator) Translate(_ tool.Context, call llm.ToolCall) tool.CallStatus {
	var args struct {
		Evidence string `json:"evidence"`
		Blocker  string `json:"blocker"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return tool.CallStatus{Error: "invalid goal tool arguments: " + err.Error()}
	}
	var err error
	if t.name == "GoalComplete" {
		_, err = t.store.Complete(context.Background(), t.sessionID, args.Evidence)
	} else {
		_, err = t.store.ReportBlocker(context.Background(), t.sessionID, t.turnID, args.Blocker)
	}
	if err != nil {
		return tool.CallStatus{Error: err.Error()}
	}
	return tool.CallStatus{}
}
func (t goalTranslator) TranslateResult(callID string, status tool.CallStatus, _ []operation.Operation) (llm.ToolResult, error) {
	message := fmt.Sprintf("Goal status recorded by %s.", t.name)
	if status.Error != "" {
		message = status.Error
	}
	return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: message}}}, nil
}
