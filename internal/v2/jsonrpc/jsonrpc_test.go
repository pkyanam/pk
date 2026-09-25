package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// serveOne sends one request line to a Server with the given handlers and
// returns the raw response line(s). It closes the reader so Serve returns.
func serveOne(handlers map[string]func(Params) (any, error), input string) (string, error) {
	var out bytes.Buffer
	s := &Server{Handlers: handlers}
	err := s.Serve(strings.NewReader(input), &out)
	return out.String(), err
}

func TestRequestResponseRoundtrip(t *testing.T) {
	handlers := map[string]func(Params) (any, error){
		"ping": func(Params) (any, error) { return "pong", nil },
	}
	out, err := serveOne(handlers, `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if resp.JSONRPC != "2.0" || resp.ID != float64(1) || resp.Result != "pong" || resp.Error != nil {
		t.Fatalf("response = %+v, want jsonrpc 2.0 id 1 result pong", resp)
	}
}

func TestMethodNotFound(t *testing.T) {
	out, err := serveOne(nil, `{"jsonrpc":"2.0","id":7,"method":"nope"}`+"\n")
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if resp.Error == nil || resp.Error.Code != MethodNotFound {
		t.Fatalf("error = %+v, want code %d", resp.Error, MethodNotFound)
	}
	if resp.ID != float64(7) {
		t.Fatalf("id = %v, want 7", resp.ID)
	}
}

func TestParseError(t *testing.T) {
	out, err := serveOne(nil, `{"jsonrpc":"2.0","id":1,"method":"ping"`+"\n") // unterminated JSON
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if resp.Error == nil || resp.Error.Code != ParseError {
		t.Fatalf("error = %+v, want code %d", resp.Error, ParseError)
	}
}

func TestInvalidRequest(t *testing.T) {
	// Valid JSON that is not an object is an invalid request, not a parse error.
	out, err := serveOne(nil, `42`+"\n")
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if resp.Error == nil || resp.Error.Code != InvalidRequest {
		t.Fatalf("error = %+v, want code %d", resp.Error, InvalidRequest)
	}
}

func TestInvalidParamsFromHandler(t *testing.T) {
	handlers := map[string]func(Params) (any, error){
		"echo": func(Params) (any, error) {
			return nil, &RPCError{Code: InvalidParams, Message: "bad params"}
		},
	}
	out, err := serveOne(handlers, `{"jsonrpc":"2.0","id":3,"method":"echo","params":{"x":1}}`+"\n")
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if resp.Error == nil || resp.Error.Code != InvalidParams || resp.Error.Message != "bad params" {
		t.Fatalf("error = %+v, want code %d message %q", resp.Error, InvalidParams, "bad params")
	}
}

func TestInternalErrorFromHandler(t *testing.T) {
	handlers := map[string]func(Params) (any, error){
		"boom": func(Params) (any, error) { return nil, errors.New("kaboom") },
	}
	out, err := serveOne(handlers, `{"jsonrpc":"2.0","id":4,"method":"boom"}`+"\n")
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if resp.Error == nil || resp.Error.Code != InternalError {
		t.Fatalf("error = %+v, want code %d", resp.Error, InternalError)
	}
}

func TestNotificationDispatchedNotAnswered(t *testing.T) {
	called := false
	handlers := map[string]func(Params) (any, error){
		"ping": func(Params) (any, error) { called = true; return "pong", nil },
	}
	out, err := serveOne(handlers, `{"jsonrpc":"2.0","method":"ping"}`+"\n")
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if !called {
		t.Fatal("notification was not dispatched")
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("notification produced a response: %q", out)
	}
}

func TestParamsPassedToHandler(t *testing.T) {
	var got Params
	handlers := map[string]func(Params) (any, error){
		"echo": func(p Params) (any, error) {
			got = p
			var s string
			if err := json.Unmarshal(p, &s); err != nil {
				return nil, err
			}
			return s, nil
		},
	}
	out, err := serveOne(handlers, `{"jsonrpc":"2.0","id":5,"method":"echo","params":"hello"}`+"\n")
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	if string(got) != `"hello"` {
		t.Fatalf("handler params = %s, want %q", got, `"hello"`)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if resp.Result != "hello" {
		t.Fatalf("result = %v, want hello", resp.Result)
	}
}

func TestMultipleRequestsAndBlankLines(t *testing.T) {
	handlers := map[string]func(Params) (any, error){
		"ping": func(Params) (any, error) { return "pong", nil },
	}
	input := "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" +
		"\n" +
		`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"
	out, err := serveOne(handlers, input)
	if err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d response lines %q, want 2", len(lines), out)
	}
	for i, line := range lines {
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
		if resp.Result != "pong" {
			t.Fatalf("line %d result = %v, want pong", i, resp.Result)
		}
	}
}