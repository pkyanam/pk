package interaction

import (
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

const AskUserName = "AskUser"

var askUserDefinition = tool.Definition{Tool: llm.Tool{
	Type:        llm.ToolFunction,
	Name:        AskUserName,
	Description: "Ask the user a concise clarifying question or confirmation when their answer will change your work. Use this when blocked on a missing detail or preference. This tool is not an approval gate for ordinary tool use.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string", "description": "The question for the user."},
			"choices":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional answer choices. The user may also answer in their own words."},
			"kind":     map[string]any{"type": "string", "enum": []string{"question", "confirmation"}, "description": "Use confirmation for a yes/no decision."},
		},
		"required": []string{"question"},
	},
}}

type Registry struct {
	base   tool.Registry
	broker *Broker
	ask    *askUserTranslator
}

// DecorateRegistry adds the user-question tool only when a foreground broker
// is present. Headless and detached runs leave the registry unchanged.
func DecorateRegistry(base tool.Registry, broker *Broker) tool.Registry {
	if broker == nil || base == nil {
		return base
	}
	if _, exists := base.Resolve(AskUserName); exists {
		return base
	}
	return &Registry{base: base, broker: broker, ask: &askUserTranslator{broker: broker}}
}

func (r *Registry) StaticDefinitions() []tool.Definition {
	defs := append([]tool.Definition(nil), r.base.StaticDefinitions()...)
	defs = append(defs, askUserDefinition)
	return defs
}
func (r *Registry) Resolve(name string) (tool.Translator, bool) {
	if name == AskUserName {
		return r.ask, true
	}
	return r.base.Resolve(name)
}
func (r *Registry) RegisterSkill(skill tool.Skill) (tool.RegistrationID, error) {
	return r.base.RegisterSkill(skill)
}
func (r *Registry) UnregisterSkill(id tool.RegistrationID) { r.base.UnregisterSkill(id) }
func (r *Registry) Skills() []tool.Skill                   { return r.base.Skills() }

type askUserTranslator struct{ broker *Broker }
type askUserArguments struct {
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
	Kind     string   `json:"kind"`
}

func (t *askUserTranslator) Translate(callContext tool.Context, call llm.ToolCall) tool.CallStatus {
	if callContext == nil {
		return tool.CallStatus{Error: "AskUser: tool context is unavailable"}
	}
	var args askUserArguments
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return tool.CallStatus{Error: fmt.Sprintf("AskUser: invalid arguments: %v", err)}
	}
	args.Question = strings.TrimSpace(args.Question)
	if args.Question == "" {
		return tool.CallStatus{Error: "AskUser: question must not be empty"}
	}
	if args.Kind == "" {
		args.Kind = "question"
	}
	if args.Kind != "question" && args.Kind != "confirmation" {
		return tool.CallStatus{Error: "AskUser: kind must be question or confirmation"}
	}
	if args.Kind == "confirmation" && len(args.Choices) == 0 {
		args.Choices = []string{"Yes", "No"}
	}
	for i := range args.Choices {
		args.Choices[i] = strings.TrimSpace(args.Choices[i])
		if args.Choices[i] == "" {
			return tool.CallStatus{Error: "AskUser: choices must not be empty"}
		}
	}
	if call.CallID == "" {
		return tool.CallStatus{Error: "AskUser: stable tool call id is required"}
	}
	answer, err := t.broker.ask(Question{ID: call.CallID, Text: args.Question, Choices: args.Choices, SessionID: t.broker.sessionID, Kind: args.Kind})
	if err != nil {
		return tool.CallStatus{Error: "AskUser did not receive an answer: " + err.Error()}
	}
	encoded, err := json.Marshal(answer)
	if err != nil {
		return tool.CallStatus{Error: "AskUser: encode answer: " + err.Error()}
	}
	spec, err := operation.NewValueSpec(jsontext.Value(encoded))
	if err != nil {
		return tool.CallStatus{Error: "AskUser: encode result: " + err.Error()}
	}
	id := callContext.Submit(spec)
	return tool.CallStatus{WaitingFor: []operation.ID{id}}
}

func (t *askUserTranslator) TranslateResult(callID string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	if status.Error != "" {
		return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: status.Error}}}, nil
	}
	if len(operations) != 1 {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("AskUser expected one value operation, got %d", len(operations))
	}
	encoded, err := operation.DecodeValue(operations[0])
	if err != nil {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("AskUser decode answer: %w", err)
	}
	var answer string
	if err := json.Unmarshal([]byte(encoded), &answer); err != nil {
		return llm.ToolResult{CallID: callID}, fmt.Errorf("AskUser answer is not a string: %w", err)
	}
	return llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: answer}}}, nil
}
