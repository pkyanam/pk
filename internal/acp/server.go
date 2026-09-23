// Package acp implements pk's stdio subset of Agent Client Protocol v1.
package acp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const ProtocolVersion = 1

type Turn struct {
	SessionID string // pk's durable session ID; empty for a session's first turn
	Prompt    string
	Workspace string
	Model     string
	Effort    string
}

type TurnResult struct{ SessionID string }

// Update describes the ACP subset emitted by a turn runner. Assistant text is
// complete at response boundaries; tool updates may arrive while the turn runs.
type Update struct {
	Kind       string
	MessageID  string
	Text       string
	ToolCallID string
	Title      string
	ToolKind   string
	Status     string
	Content    []any
}

type RunFunc func(context.Context, Turn, func(Update) error) (TurnResult, error)

type Config struct {
	Model, Effort string
	Run           RunFunc
	MaxLineBytes  int
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}
type session struct {
	id, workspace, runnerID string
	cancel                  context.CancelFunc
	active                  bool
}

type Server struct {
	cfg         Config
	in          io.Reader
	out         io.Writer
	writeMu     sync.Mutex
	mu          sync.Mutex
	initialized bool
	sessions    map[string]*session
	turns       sync.WaitGroup
}

func NewServer(input io.Reader, output io.Writer, cfg Config) (*Server, error) {
	if input == nil || output == nil {
		return nil, errors.New("ACP input and output are required")
	}
	if cfg.Run == nil {
		return nil, errors.New("ACP turn runner is required")
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 4 << 20
	}
	return &Server{cfg: cfg, in: input, out: output, sessions: make(map[string]*session)}, nil
}

// Serve reads newline-delimited JSON-RPC and returns when the transport closes.
// Prompt execution runs concurrently with reads so cancellation notifications
// are processed while the model or tools are active.
func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 4096), s.cfg.MaxLineBytes)
	type scanResult struct {
		line []byte
		err  error
		done bool
	}
	lines := make(chan scanResult, 1)
	go func() {
		for scanner.Scan() {
			value := scanResult{line: append([]byte(nil), scanner.Bytes()...)}
			select {
			case lines <- value:
			case <-ctx.Done():
				return
			}
		}
		select {
		case lines <- scanResult{err: scanner.Err(), done: true}:
		case <-ctx.Done():
		}
	}()
	for {
		select {
		case <-ctx.Done():
			s.cancelSessions()
			s.turns.Wait()
			return ctx.Err()
		case item := <-lines:
			if item.done {
				s.cancelSessions()
				s.turns.Wait()
				return item.err
			}
			var req request
			if err := json.Unmarshal(item.line, &req); err != nil {
				_ = s.write(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
				continue
			}
			if req.JSONRPC != "2.0" || req.Method == "" {
				if len(req.ID) > 0 {
					_ = s.replyError(req, -32600, "invalid request")
				}
				continue
			}
			if req.Method == "session/cancel" {
				s.handleCancel(req)
				continue
			}
			if err := s.handle(ctx, req); err != nil && len(req.ID) > 0 {
				var rpc *rpcError
				if errors.As(err, &rpc) {
					_ = s.replyError(req, rpc.Code, rpc.Message)
				} else {
					_ = s.replyError(req, -32000, err.Error())
				}
			}
		}
	}
}

func (s *Server) cancelSessions() {
	s.mu.Lock()
	for _, session := range s.sessions {
		if session.cancel != nil {
			session.cancel()
		}
	}
	s.mu.Unlock()
}

func (s *Server) handle(ctx context.Context, req request) error {
	switch req.Method {
	case "initialize":
		if len(req.ID) == 0 {
			return nil
		}
		var p struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcErrf(-32602, "invalid initialize parameters")
		}
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
		return s.reply(req, map[string]any{"protocolVersion": ProtocolVersion, "agentCapabilities": map[string]any{"promptCapabilities": map[string]any{}, "sessionCapabilities": map[string]any{}}, "agentInfo": map[string]string{"name": "pk", "title": "pk", "version": "dev"}, "authMethods": []any{}})
	case "session/new":
		if len(req.ID) == 0 { return nil }
		if !s.isInitialized() {
			return rpcErrf(-32002, "initialize must be called before session/new")
		}
		var p struct {
			CWD        string            `json:"cwd"`
			MCPServers []json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcErrf(-32602, "invalid session/new parameters")
		}
		if len(p.MCPServers) > 0 {
			return rpcErrf(-32602, "pk ACP does not support MCP server declarations")
		}
		if !filepath.IsAbs(p.CWD) {
			return rpcErrf(-32602, "cwd must be an absolute path")
		}
		info, err := os.Stat(p.CWD)
		if err != nil || !info.IsDir() {
			return rpcErrf(-32602, "cwd must be an existing directory")
		}
		id, err := newID()
		if err != nil {
			return fmt.Errorf("create ACP session ID: %w", err)
		}
		s.mu.Lock()
		s.sessions[id] = &session{id: id, workspace: filepath.Clean(p.CWD)}
		s.mu.Unlock()
		return s.reply(req, map[string]string{"sessionId": id})
	case "session/prompt":
		if len(req.ID) == 0 { return nil }
		if !s.isInitialized() {
			return rpcErrf(-32002, "initialize must be called before session/prompt")
		}
		var p struct {
			SessionID string            `json:"sessionId"`
			Prompt    []json.RawMessage `json:"prompt"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcErrf(-32602, "invalid session/prompt parameters")
		}
		text, err := promptText(p.Prompt)
		if err != nil {
			return &rpcError{Code: -32602, Message: err.Error()}
		}
		if strings.TrimSpace(text) == "" {
			return rpcErrf(-32602, "prompt must contain text")
		}
		s.mu.Lock()
		ss := s.sessions[p.SessionID]
		if ss == nil {
			s.mu.Unlock()
			return rpcErrf(-32001, "unknown session")
		}
		if ss.active {
			s.mu.Unlock()
			return rpcErrf(-32000, "session already has an active prompt")
		}
		turnCtx, cancel := context.WithCancel(ctx)
		ss.active = true
		ss.cancel = cancel
		turn := Turn{SessionID: ss.runnerID, Prompt: text, Workspace: ss.workspace, Model: s.cfg.Model, Effort: s.cfg.Effort}
		s.mu.Unlock()
		s.turns.Add(1)
		go func() { defer s.turns.Done(); s.runPrompt(turnCtx, cancel, req, ss, turn) }()
		return nil
	default:
		if len(req.ID) == 0 {
			return nil
		}
		return &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) runPrompt(ctx context.Context, cancel context.CancelFunc, req request, ss *session, turn Turn) {
	messageID, _ := newID()
	_ = s.sendUpdate(ss.id, map[string]any{"sessionUpdate": "user_message_chunk", "messageId": messageID, "content": map[string]any{"type": "text", "text": turn.Prompt}})
	result, err := s.cfg.Run(ctx, turn, func(update Update) error { return s.emitUpdate(ss.id, update) })
	cancelled := errors.Is(ctx.Err(), context.Canceled)
	s.mu.Lock()
	ss.active = false
	ss.cancel = nil
	if result.SessionID != "" {
		ss.runnerID = result.SessionID
	}
	s.mu.Unlock()
	cancel()
	if cancelled {
		_ = s.reply(req, map[string]string{"stopReason": "cancelled"})
		return
	}
	if err != nil {
		_ = s.replyError(req, -32000, err.Error())
		return
	}
	_ = s.reply(req, map[string]string{"stopReason": "end_turn"})
}

func (s *Server) handleCancel(req request) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		return
	}
	s.mu.Lock()
	ss := s.sessions[p.SessionID]
	if ss != nil && ss.cancel != nil {
		ss.cancel()
	}
	s.mu.Unlock()
}

func (s *Server) emitUpdate(sessionID string, u Update) error {
	var data map[string]any
	switch u.Kind {
	case "assistant":
		if u.Text == "" {
			return nil
		}
		if u.MessageID == "" {
			u.MessageID, _ = newID()
		}
		data = map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": u.MessageID, "content": map[string]string{"type": "text", "text": u.Text}}
	case "tool_call":
		data = map[string]any{"sessionUpdate": "tool_call", "toolCallId": u.ToolCallID, "title": u.Title, "kind": defaultToolKind(u.ToolKind), "status": defaultStatus(u.Status)}
	case "tool_call_update":
		data = map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": u.ToolCallID, "status": defaultStatus(u.Status)}
		if len(u.Content) > 0 {
			data["content"] = u.Content
		}
	default:
		return nil
	}
	return s.sendUpdate(sessionID, data)
}

func (s *Server) sendUpdate(sessionID string, update map[string]any) error {
	return s.write(notification{JSONRPC: "2.0", Method: "session/update", Params: map[string]any{"sessionId": sessionID, "update": update}})
}
func (s *Server) reply(req request, result any) error {
	return s.write(response{JSONRPC: "2.0", ID: req.ID, Result: result})
}
func (s *Server) replyError(req request, code int, message string) error {
	return s.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: message}})
}
func (s *Server) write(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return json.NewEncoder(s.out).Encode(value)
}
func (s *Server) isInitialized() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.initialized }

func promptText(blocks []json.RawMessage) (string, error) {
	var chunks []string
	for _, raw := range blocks {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
			URI  string `json:"uri"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return "", errors.New("invalid prompt content block")
		}
		switch block.Type {
		case "text":
			chunks = append(chunks, block.Text)
		case "resource_link":
			if block.URI == "" {
				return "", errors.New("resource_link requires uri")
			}
			label := block.Name
			if label == "" {
				label = block.URI
			}
			chunks = append(chunks, "[User referenced resource: "+label+" — "+block.URI+"]")
		default:
			return "", fmt.Errorf("unsupported prompt content type %q (pk supports text and resource_link)", block.Type)
		}
	}
	return strings.Join(chunks, "\n"), nil
}
func defaultToolKind(value string) string {
	switch value {
	case "read", "edit", "delete", "move", "search", "execute", "think", "fetch", "other":
		return value
	default:
		return "other"
	}
}
func defaultStatus(value string) string {
	switch value {
	case "pending", "in_progress", "completed", "failed", "cancelled":
		return value
	default:
		return "in_progress"
	}
}
func rpcErrf(code int, format string, args ...any) error {
	return &rpcError{Code: code, Message: fmt.Sprintf(format, args...)}
}
func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "pk_" + hex.EncodeToString(b[:]), nil
}
