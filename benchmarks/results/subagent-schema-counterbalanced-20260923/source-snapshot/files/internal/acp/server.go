// Package acp implements pk's stdio subset of Agent Client Protocol v1.
package acp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
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

// MaxInlineImageBytes keeps base64-expanded image blocks plus JSON framing
// below the default 4 MiB ACP line limit.
const MaxInlineImageBytes = 2 << 20
const MaxInlineImages = 8
const MaxConcurrentImagePrompts = 8

type PromptBlock struct {
	Type, Text, URI, Name, MIMEType string
	ImageData                       []byte
}

// UserContent projects accepted prompt blocks back to ACP. Inline image data
// is tool-mediated for the model, not a native provider image message.
func UserContent(blocks []PromptBlock) []any {
	content := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": block.Text})
		case "resource_link":
			label := block.Name
			if label == "" {
				label = block.URI
			}
			content = append(content, map[string]any{"type": "text", "text": "[User referenced resource: " + label + " — " + block.URI + "]"})
		case "image":
			content = append(content, map[string]any{"type": "image", "mimeType": block.MIMEType, "data": base64.StdEncoding.EncodeToString(block.ImageData)})
		}
	}
	return content
}

type Turn struct {
	SessionID string // pk's durable session ID; empty for a session's first turn
	Prompt    string
	Workspace string
	Model     string
	Effort    string
	Blocks    []PromptBlock // ordered ACP blocks; ImageData is memory-only
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
	// NewSession reserves the ACP session ID in the backing durable store.
	// When configured, the ACP ID is also the runner session ID.
	NewSession func(context.Context, string, string) error
	// LoadSession validates and replays a durable session before session/load
	// returns. It must emit history through emit in chronological order.
	LoadSession  func(context.Context, string, string, func(Update) error) error
	MaxLineBytes int
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
	imagesActive            bool
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
		if p.ProtocolVersion < 1 {
			return rpcErrf(-32602, "initialize requires a positive protocolVersion")
		}
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
		loadSession := s.cfg.LoadSession != nil
		return s.reply(req, map[string]any{"protocolVersion": ProtocolVersion, "agentCapabilities": map[string]any{"loadSession": loadSession, "promptCapabilities": map[string]any{"image": true}, "sessionCapabilities": map[string]any{}}, "agentInfo": map[string]string{"name": "pk", "title": "pk", "version": "dev"}, "authMethods": []any{}})
	case "session/new":
		if len(req.ID) == 0 {
			return nil
		}
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
		runnerID := ""
		if s.cfg.NewSession != nil {
			if err := s.cfg.NewSession(ctx, id, filepath.Clean(p.CWD)); err != nil {
				return rpcErrf(-32000, "create durable session: %v", err)
			}
			runnerID = id
		}
		s.mu.Lock()
		s.sessions[id] = &session{id: id, workspace: filepath.Clean(p.CWD), runnerID: runnerID}
		s.mu.Unlock()
		return s.reply(req, map[string]string{"sessionId": id})
	case "session/load":
		if len(req.ID) == 0 {
			return nil
		}
		if !s.isInitialized() {
			return rpcErrf(-32002, "initialize must be called before session/load")
		}
		if s.cfg.LoadSession == nil {
			return rpcErrf(-32601, "session/load is not supported")
		}
		var p struct {
			SessionID  string            `json:"sessionId"`
			CWD        string            `json:"cwd"`
			MCPServers []json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.SessionID) == "" {
			return rpcErrf(-32602, "invalid session/load parameters")
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
		s.mu.Lock()
		if existing := s.sessions[p.SessionID]; existing != nil {
			s.mu.Unlock()
			return rpcErrf(-32000, "session is already active")
		}
		s.mu.Unlock()
		workspace := filepath.Clean(p.CWD)
		err = s.cfg.LoadSession(ctx, p.SessionID, workspace, func(update Update) error {
			return s.emitUpdate(p.SessionID, update)
		})
		if err != nil {
			return rpcErrf(-32001, "load session: %v", err)
		}
		s.mu.Lock()
		s.sessions[p.SessionID] = &session{id: p.SessionID, workspace: workspace, runnerID: p.SessionID}
		s.mu.Unlock()
		return s.reply(req, map[string]any{})
	case "session/prompt":
		if len(req.ID) == 0 {
			return nil
		}
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
		blocks, text, err := parsePromptBlocks(p.Prompt)
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
		hasImages := false
		for _, block := range blocks {
			if block.Type == "image" {
				hasImages = true
				break
			}
		}
		if hasImages {
			activeImagePrompts := 0
			for _, activeSession := range s.sessions {
				if activeSession.imagesActive {
					activeImagePrompts++
				}
			}
			if activeImagePrompts >= MaxConcurrentImagePrompts {
				s.mu.Unlock()
				return rpcErrf(-32000, "too many concurrent inline-image prompts; limit is %d", MaxConcurrentImagePrompts)
			}
		}
		turnCtx, cancel := context.WithCancel(ctx)
		ss.active = true
		ss.imagesActive = hasImages
		ss.cancel = cancel
		turn := Turn{SessionID: ss.runnerID, Prompt: text, Blocks: blocks, Workspace: ss.workspace, Model: s.cfg.Model, Effort: s.cfg.Effort}
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
	content := UserContent(turn.Blocks)
	if len(content) == 0 {
		content = []any{map[string]any{"type": "text", "text": turn.Prompt}}
	}
	for _, block := range content {
		_ = s.sendUpdate(ss.id, map[string]any{"sessionUpdate": "user_message_chunk", "messageId": messageID, "content": block})
	}
	result, err := s.cfg.Run(ctx, turn, func(update Update) error { return s.emitUpdate(ss.id, update) })
	cancelled := errors.Is(ctx.Err(), context.Canceled)
	s.mu.Lock()
	ss.active = false
	ss.imagesActive = false
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
	case "user":
		if u.Text == "" && len(u.Content) == 0 {
			return nil
		}
		if u.MessageID == "" {
			u.MessageID, _ = newID()
		}
		content := u.Content
		if len(content) == 0 {
			content = []any{map[string]string{"type": "text", "text": u.Text}}
		}
		for _, block := range content {
			if err := s.sendUpdate(sessionID, map[string]any{"sessionUpdate": "user_message_chunk", "messageId": u.MessageID, "content": block}); err != nil {
				return err
			}
		}
		return nil
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
	_, text, err := parsePromptBlocks(blocks)
	return text, err
}

func parsePromptBlocks(rawBlocks []json.RawMessage) ([]PromptBlock, string, error) {
	if len(rawBlocks) > 256 {
		return nil, "", errors.New("prompt has too many content blocks")
	}
	blocks := make([]PromptBlock, 0, len(rawBlocks))
	var chunks []string
	var imageBytes int
	var imageCount int
	for _, raw := range rawBlocks {
		var block PromptBlock
		var wire struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			URI      string `json:"uri"`
			Name     string `json:"name"`
			Data     string `json:"data"`
			MIMEType string `json:"mimeType"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, "", errors.New("invalid prompt content block")
		}
		block.Type, block.Text, block.URI, block.Name, block.MIMEType = wire.Type, wire.Text, wire.URI, wire.Name, wire.MIMEType
		switch wire.Type {
		case "text":
			chunks = append(chunks, wire.Text)
		case "resource_link":
			if wire.URI == "" {
				return nil, "", errors.New("resource_link requires uri")
			}
			label := wire.Name
			if label == "" {
				label = wire.URI
			}
			chunks = append(chunks, "[User referenced resource: "+label+" — "+wire.URI+"]")
		case "image":
			if imageCount >= MaxInlineImages {
				return nil, "", fmt.Errorf("too many inline images; limit is %d", MaxInlineImages)
			}
			if !supportedImageMIME(wire.MIMEType) {
				return nil, "", fmt.Errorf("unsupported inline image MIME type %q", wire.MIMEType)
			}
			if wire.Data == "" || len(wire.Data) > base64.StdEncoding.EncodedLen(MaxInlineImageBytes) || strings.ContainsAny(wire.Data, " \t\r\n") {
				return nil, "", errors.New("inline image data is empty or exceeds the ACP image limit")
			}
			data, err := base64.StdEncoding.Strict().DecodeString(wire.Data)
			if err != nil || len(data) == 0 {
				return nil, "", errors.New("inline image data must be valid standard base64")
			}
			if len(data) > MaxInlineImageBytes-imageBytes {
				return nil, "", fmt.Errorf("inline images exceed the %d-byte aggregate limit", MaxInlineImageBytes)
			}
			imageBytes += len(data)
			imageCount++
			block.ImageData = data
			chunks = append(chunks, fmt.Sprintf("[Inline image (%s); inspect it with ViewImage if needed.]", wire.MIMEType))
		default:
			return nil, "", fmt.Errorf("unsupported prompt content type %q (pk supports text, image, and resource_link)", wire.Type)
		}
		blocks = append(blocks, block)
	}
	return blocks, strings.Join(chunks, "\n"), nil
}

func supportedImageMIME(value string) bool {
	switch value {
	case "image/png", "image/jpeg", "image/webp", "image/bmp", "image/tiff":
		return true
	default:
		return false
	}
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
	return "pk-" + hex.EncodeToString(b[:]), nil
}
