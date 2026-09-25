package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkyanam/pk/internal/providers"
	"github.com/pkyanam/pk/internal/v2/jsonrpc"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// sessionCreateParams is the JSON-RPC params for session/create.
type sessionCreateParams struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// sessionCreate creates a new session and returns its ID. The provider/model
// default to the configured defaults when empty.
func (d *Daemon) sessionCreate(params jsonrpc.Params) (any, error) {
	var p sessionCreateParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	provider, err := d.resolveProvider(p.Provider)
	if err != nil {
		return nil, err
	}
	model := p.Model
	if model == "" {
		model = provider.DefaultModel
	}
	if model == "" {
		model = "gpt-6-luna"
	}
	s := &Session{
		ID:       newSessionID(),
		Provider: provider.ID,
		Model:    model,
	}
	d.mu.Lock()
	d.sessions[s.ID] = s
	d.mu.Unlock()
	if err := d.saveSession(s); err != nil {
		return nil, err
	}
	return map[string]any{"id": s.ID, "provider": s.Provider, "model": s.Model}, nil
}

// sessionAttachParams is the JSON-RPC params for session/attach.
type sessionAttachParams struct {
	ID string `json:"id"`
}

// sessionAttach loads a persisted session into memory for continued turns.
func (d *Daemon) sessionAttach(params jsonrpc.Params) (any, error) {
	var p sessionAttachParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.ID) == "" {
		return nil, jsonrpc.ErrInvalidParams("session/attach requires an id")
	}
	s, err := d.loadSession(p.ID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %q not found", p.ID)
	}
	d.mu.Lock()
	d.sessions[s.ID] = s
	d.mu.Unlock()
	return map[string]any{"id": s.ID, "provider": s.Provider, "model": s.Model, "items": len(s.Items)}, nil
}

// promptParams is the JSON-RPC params for prompt.
type promptParams struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// prompt runs one bounded agent turn in the session: it appends the user
// message, drives the model loop (calling builtin tools when requested), and
// returns the final assistant text plus the tool calls made. This is the
// "write a tool -> invoke it the same turn" demo path for core (Go) tools;
// the Bun extension layer hooks in later (internal/v2/ext).
func (d *Daemon) prompt(params jsonrpc.Params) (any, error) {
	var p promptParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return nil, jsonrpc.ErrInvalidParams("prompt requires session_id")
	}
	if strings.TrimSpace(p.Text) == "" {
		return nil, jsonrpc.ErrInvalidParams("prompt requires text")
	}
	d.mu.Lock()
	s, ok := d.sessions[p.SessionID]
	if !ok {
		d.mu.Unlock()
		// Reconnect path: session/attach loads it; a prompt on an unknown id
		// is an error (the client should attach first).
		return nil, fmt.Errorf("unknown session %q (create or attach first)", p.SessionID)
	}
	d.mu.Unlock()

	result, err := d.runTurn(s, p.Text)
	if err != nil {
		return nil, err
	}
	if err := d.saveSession(s); err != nil {
		return nil, err
	}
	return result, nil
}

// runTurn drives the model loop for one user message. It builds the llm
// request from the session's items plus tools, calls the provider, executes
// any requested tool calls against the builtin tool set, appends results, and
// repeats until the model finishes.
func (d *Daemon) runTurn(s *Session, text string) (map[string]any, error) {
	provider, err := d.resolveProvider(s.Provider)
	if err != nil {
		return nil, err
	}
	var client llm.Adapter
	if d.providerFactory != nil {
		client, err = d.providerFactory()
		if err != nil {
			return nil, err
		}
	} else {
		pc, perr := providers.NewClient(provider)
		if perr != nil {
			return nil, fmt.Errorf("provider client: %w", perr)
		}
		client = pc
		defer pc.Close()
	}

	// Append the user message to the session transcript.
	msg, err := messageItem(llm.RoleUser, text)
	if err != nil {
		return nil, err
	}
	s.Items = append(s.Items, msg)

	request := llm.Request{
		Model: llm.Model{ID: s.Model},
		Input: decodeItems(s.Items),
		Tools: builtinTools(),
	}

	var toolCalls []map[string]any
	maxTurns := 8
	for turn := 0; turn < maxTurns; turn++ {
		response, err := client.Respond(context.Background(), request, llm.RequestOptions{CacheKey: s.ID})
		if err != nil {
			return nil, fmt.Errorf("provider response: %w", err)
		}
		if response.Failure != nil {
			return nil, fmt.Errorf("provider failure: %s: %s", response.Failure.Code, response.Failure.Message)
		}
		// Append assistant output items.
		for _, item := range response.Output {
			ji, err := encodeItem(item)
			if err != nil {
				return nil, err
			}
			s.Items = append(s.Items, ji)
		}
		// Find any tool calls in this response.
		calls := responseToolCalls(response.Output)
		if len(calls) == 0 {
			// Done: return the final assistant text.
			return map[string]any{
				"session_id": s.ID,
				"text":       assistantText(response.Output),
				"tool_calls": toolCalls,
				"stop":       string(response.Stop),
			}, nil
		}
		// Execute each requested tool call and append results.
		for _, call := range calls {
			args := map[string]any{}
			if call.Arguments != "" {
				_ = json.Unmarshal([]byte(call.Arguments), &args)
			}
			output, err := invokeBuiltin(call.Name, args)
			kind := llm.ToolResultText
			value := output
			if err != nil {
				value = "error: " + err.Error()
			}
			ri, riErr := toolResultItem(call.CallID, kind, value)
			if riErr != nil {
				return nil, riErr
			}
			s.Items = append(s.Items, ri)
			toolCalls = append(toolCalls, map[string]any{
				"name":   call.Name,
				"args":   call.Arguments,
				"result": value,
			})
		}
		// Next iteration includes the tool results.
		request.Input = decodeItems(s.Items)
	}
	return nil, fmt.Errorf("agent exceeded %d tool turns", maxTurns)
}

// helpers below convert between llm.Items and the persisted jsonItem form.

func decodeItems(items []jsonItem) []llm.Item {
	out := make([]llm.Item, 0, len(items))
	for _, ji := range items {
		var data any
		switch ji.Type {
		case string(llm.ItemMessage):
			var m llm.Message
			if json.Unmarshal(ji.Data, &m) == nil {
				data = m
			}
		case string(llm.ItemToolCall):
			var tc llm.ToolCall
			if json.Unmarshal(ji.Data, &tc) == nil {
				data = tc
			}
		case string(llm.ItemToolResult):
			var tr llm.ToolResult
			if json.Unmarshal(ji.Data, &tr) == nil {
				data = tr
			}
		}
		if data != nil {
			out = append(out, llm.Item{Type: llm.ItemType(ji.Type), Data: data})
		}
	}
	return out
}

func encodeItem(item llm.Item) (jsonItem, error) {
	b, err := json.Marshal(item.Data)
	if err != nil {
		return jsonItem{}, err
	}
	return jsonItem{Type: string(item.Type), Data: b}, nil
}

func messageItem(role llm.Role, text string) (jsonItem, error) {
	b, err := json.Marshal(llm.Message{Role: role, Text: text})
	if err != nil {
		return jsonItem{}, err
	}
	return jsonItem{Type: string(llm.ItemMessage), Data: b}, nil
}

func toolResultItem(callID string, kind llm.ToolResultKind, value string) (jsonItem, error) {
	b, err := json.Marshal(llm.ToolResult{CallID: callID, Output: []llm.ToolResultOutput{{Kind: kind, Value: value}}})
	if err != nil {
		return jsonItem{}, err
	}
	return jsonItem{Type: string(llm.ItemToolResult), Data: b}, nil
}

// responseToolCalls extracts tool calls from a provider response's output.
func responseToolCalls(output []llm.Item) []llm.ToolCall {
	var calls []llm.ToolCall
	for _, item := range output {
		if item.Type != llm.ItemToolCall {
			continue
		}
		if tc, ok := item.Data.(llm.ToolCall); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// assistantText joins the assistant text items of a response.
func assistantText(output []llm.Item) string {
	var parts []string
	for _, item := range output {
		if item.Type == llm.ItemMessage {
			if m, ok := item.Data.(llm.Message); ok && m.Role == llm.RoleAssistant {
				parts = append(parts, m.Text)
			}
		}
	}
	return strings.Join(parts, "")
}

func newSessionID() string {
	return fmt.Sprintf("pk2-%d", time.Now().UnixNano())
}

func decodeParams(params jsonrpc.Params, dst any) error {
	if len(params) == 0 {
		return jsonrpc.ErrInvalidParams("missing params")
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return jsonrpc.ErrInvalidParams("invalid params: " + err.Error())
	}
	return nil
}
