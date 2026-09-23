package extensions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

type InitializeParams struct {
	APIVersion   string   `json:"api_version"`
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Workspace    string   `json:"workspace"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type InitializeResult struct {
	APIVersion string   `json:"api_version"`
	ID         string   `json:"id"`
	Tools      []string `json:"tools,omitempty"`
	Commands   []string `json:"commands,omitempty"`
}

type ToolCallParams struct {
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
	Workspace string          `json:"workspace"`
}

type CommandCallParams struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Workspace string `json:"workspace"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolResult struct {
	Content []Content       `json:"content"`
	Details json.RawMessage `json:"details,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type ToolExecuteParams struct {
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
	Workspace string          `json:"workspace"`
}

type CommandExecuteParams struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Workspace string `json:"workspace"`
}

// Worker is the transport boundary. Tests can provide an in-process worker;
// production uses the JSON Lines process transport below.
type Worker interface {
	Call(context.Context, string, any, any) error
	Close() error
}

type WorkerFactory func(context.Context, Manifest, string) (Worker, error)

type Handler interface {
	Initialize(context.Context, InitializeParams) (InitializeResult, error)
	ExecuteTool(context.Context, ToolExecuteParams) (ToolResult, error)
	ExecuteCommand(context.Context, CommandExecuteParams) (string, error)
}

// Serve implements the public v1 worker protocol for extension executables.
// stdout is reserved for responses; diagnostics belong on stderr.
func Serve(ctx context.Context, input io.Reader, output io.Writer, handler Handler) error {
	if handler == nil {
		return errors.New("extension handler is required")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxMessageSize)
	writer := bufio.NewWriter(output)
	for scanner.Scan() {
		var request Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("decode extension request: %w", err)
		}
		if request.ID == "" || request.Method == "" {
			return errors.New("extension request requires id and method")
		}
		response := Response{ID: request.ID}
		var result any
		var err error
		switch request.Method {
		case "initialize":
			var params InitializeParams
			err = json.Unmarshal(request.Params, &params)
			if err == nil {
				result, err = handler.Initialize(ctx, params)
			}
		case "tool.execute":
			var params ToolExecuteParams
			err = json.Unmarshal(request.Params, &params)
			if err == nil {
				result, err = handler.ExecuteTool(ctx, params)
			}
		case "command.execute":
			var params CommandExecuteParams
			err = json.Unmarshal(request.Params, &params)
			if err == nil {
				result, err = handler.ExecuteCommand(ctx, params)
			}
		default:
			err = fmt.Errorf("unsupported method %q", request.Method)
		}
		if err != nil {
			response.Error = &RPCError{Code: "extension_error", Message: err.Error()}
		} else {
			response.Result, err = json.Marshal(result)
			if err != nil {
				return fmt.Errorf("encode extension response: %w", err)
			}
		}
		line, err := json.Marshal(response)
		if err != nil {
			return fmt.Errorf("encode extension response: %w", err)
		}
		if len(line) > maxMessageSize {
			return errors.New("extension response exceeds protocol message limit")
		}
		if _, err = writer.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("write extension response: %w", err)
		}
		if err = writer.Flush(); err != nil {
			return fmt.Errorf("flush extension response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read extension request: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func validateResponse(response Response, expectedID string, target any) error {
	if response.ID != expectedID {
		return fmt.Errorf("extension response ID %q; want %q", response.ID, expectedID)
	}
	if response.Error != nil {
		if len(response.Result) != 0 {
			return errors.New("extension response must not contain both result and error")
		}
		return response.Error
	}
	if len(response.Result) == 0 || strings.TrimSpace(string(response.Result)) == "null" {
		return errors.New("extension response has no result")
	}
	if err := json.Unmarshal(response.Result, target); err != nil {
		return fmt.Errorf("decode extension result: %w", err)
	}
	return nil
}
