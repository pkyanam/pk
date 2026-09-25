// Package jsonrpc implements a minimal JSON-RPC 2.0 server over stdio for the
// v2 daemon.
//
// Framing is newline-delimited JSON: exactly one request or response per
// line. Content-Length framing is NOT needed for this milestone — the daemon
// speaks to stateless local clients over a pipe, so each request is a single
// line and EOF ends the session. Content-Length framing (or the Unix domain
// socket / HTTP transports) can be layered on later without changing the
// request/response types.
package jsonrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// JSON-RPC 2.0 error codes.
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// Params is the raw params member of a request, passed through to handlers
// for method-specific decoding.
type Params = json.RawMessage

// Request is a JSON-RPC 2.0 request object.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response object.
type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements error so handlers can return RPC errors directly.
func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message)
}

// Server dispatches newline-delimited JSON-RPC requests to registered
// handlers. The daemon is the single writer, so no synchronization is needed.
type Server struct {
	Handlers map[string]func(Params) (any, error)
}

// Serve reads newline-delimited JSON requests from r, dispatches them to the
// registered handlers, and writes one JSON response per request to w.
// Notifications (requests without an id) are dispatched but not answered.
// It returns on read error or EOF.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		resp, send := s.handleMessage(line)
		if !send {
			continue
		}
		b, err := json.Marshal(resp)
		if err != nil {
			return fmt.Errorf("jsonrpc: marshal response: %w", err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return fmt.Errorf("jsonrpc: write response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("jsonrpc: read request: %w", err)
	}
	return nil
}

// handleMessage parses one line and produces the response (plus whether it
// should be sent). Requests with a missing or null id are treated as
// notifications: they are dispatched but produce no response.
func (s *Server) handleMessage(line []byte) (Response, bool) {
	var raw json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return errorResponse(nil, ParseError, "parse error"), true
	}
	// Valid JSON that is not an object (e.g. 42, "x", []) is an invalid
	// request, not a parse error.
	if len(raw) == 0 || raw[0] != '{' {
		return errorResponse(nil, InvalidRequest, "invalid request"), true
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return errorResponse(nil, InvalidRequest, "invalid request"), true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return errorResponse(req.ID, InvalidRequest, "invalid request"), true
	}
	if req.ID == nil {
		s.dispatch(req)
		return Response{}, false
	}
	handler, ok := s.Handlers[req.Method]
	if !ok {
		return errorResponse(req.ID, MethodNotFound, "method not found: "+req.Method), true
	}
	result, err := handler(req.Params)
	if err != nil {
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			return Response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}, true
		}
		return errorResponse(req.ID, InternalError, err.Error()), true
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: result}, true
}

func (s *Server) dispatch(req Request) {
	if handler, ok := s.Handlers[req.Method]; ok {
		_, _ = handler(req.Params)
	}
}

func errorResponse(id any, code int, message string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: message}}
}