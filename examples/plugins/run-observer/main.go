// Command run-observer demonstrates the metadata-only lifecycle observer API.
// It declares no model tools or slash commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pkyanam/pk/internal/extensions"
)

type observer struct {
	stderr io.Writer
}

func (o observer) Initialize(_ context.Context, params extensions.InitializeParams) (extensions.InitializeResult, error) {
	if params.APIVersion != extensions.ProtocolVersion || params.ID != "run-observer" {
		return extensions.InitializeResult{}, errors.New("unsupported host handshake")
	}
	result := extensions.InitializeResult{APIVersion: extensions.ProtocolVersion, ID: params.ID}
	for _, feature := range params.HostFeatures {
		if feature == extensions.HostFeatureLifecycle {
			result.Features = append(result.Features, feature)
		}
	}
	return result, nil
}

func (observer) ExecuteTool(context.Context, extensions.ToolExecuteParams) (extensions.ToolResult, error) {
	return extensions.ToolResult{}, errors.New("run-observer declares no tools")
}

func (observer) ExecuteCommand(context.Context, extensions.CommandExecuteParams) (string, error) {
	return "", errors.New("run-observer declares no commands")
}

func (o observer) NotifyLifecycle(ctx context.Context, event extensions.LifecycleEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writer := o.stderr
	if writer == nil {
		writer = os.Stderr
	}
	// Log only lifecycle metadata. In particular, omit the workspace path and
	// never read or emit prompt, response, tool argument, or tool result content.
	line := struct {
		Event     string `json:"event"`
		RunID     string `json:"run_id,omitempty"`
		SessionID string `json:"session_id,omitempty"`
		Model     string `json:"model,omitempty"`
		Status    string `json:"status,omitempty"`
	}{
		Event: event.Type, RunID: event.RunID, SessionID: event.SessionID,
		Model: event.Model, Status: event.Status,
	}
	return json.NewEncoder(writer).Encode(line)
}

func main() {
	if err := extensions.Serve(context.Background(), os.Stdin, os.Stdout, observer{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
