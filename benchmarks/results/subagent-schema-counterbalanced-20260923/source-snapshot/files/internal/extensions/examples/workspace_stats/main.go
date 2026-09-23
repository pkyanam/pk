// Command workspace-stats is a small pk extension worker. Build it before
// loading manifest.json; the manifest path is always explicitly configured.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pkyanam/pk/internal/extensions"
)

type stats struct {
	Files       int64 `json:"files"`
	Directories int64 `json:"directories"`
	Bytes       int64 `json:"bytes"`
}

type worker struct{}

func (worker) Initialize(_ context.Context, params extensions.InitializeParams) (extensions.InitializeResult, error) {
	if params.APIVersion != extensions.ProtocolVersion || params.ID != "workspace-stats" {
		return extensions.InitializeResult{}, fmt.Errorf("unsupported host handshake")
	}
	return extensions.InitializeResult{APIVersion: extensions.ProtocolVersion, ID: params.ID, Tools: []string{"workspace_stats"}, Commands: []string{"stats"}}, nil
}

func (worker) ExecuteTool(_ context.Context, params extensions.ToolExecuteParams) (extensions.ToolResult, error) {
	if params.Name != "workspace_stats" {
		return extensions.ToolResult{}, fmt.Errorf("unknown tool %q", params.Name)
	}
	var args struct{}
	if err := json.Unmarshal(params.Arguments, &args); err != nil {
		return extensions.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
	}
	result, err := scan(params.Workspace)
	if err != nil {
		return extensions.ToolResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return extensions.ToolResult{}, err
	}
	return extensions.ToolResult{Content: []extensions.Content{{Type: "text", Text: format(result)}}, Details: encoded}, nil
}

func (worker) ExecuteCommand(_ context.Context, params extensions.CommandExecuteParams) (string, error) {
	if params.Name != "stats" {
		return "", fmt.Errorf("unknown command %q", params.Name)
	}
	result, err := scan(params.Workspace)
	if err != nil {
		return "", err
	}
	return format(result), nil
}

func scan(root string) (stats, error) {
	var result stats
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			result.Directories++
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			result.Files++
			result.Bytes += info.Size()
		}
		return nil
	})
	return result, err
}

func format(value stats) string {
	return fmt.Sprintf("%d files, %d directories, %d bytes", value.Files, value.Directories, value.Bytes)
}

func main() {
	if err := extensions.Serve(context.Background(), os.Stdin, os.Stdout, worker{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
