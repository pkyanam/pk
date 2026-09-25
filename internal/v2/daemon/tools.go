package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// builtinTools is the core (Go) tool set the pk2 beta ships. This is the
// graceful-degradation baseline: when Bun is absent, these are the only tools;
// the extension layer (internal/v2/ext) registers more later.
func builtinTools() []llm.Tool {
	return []llm.Tool{
		{
			Type:        llm.ToolFunction,
			Name:        "read_file",
			Description: "Read a bounded page of a UTF-8 text file inside the workspace. Parameters: path (relative to the workspace), offset (1-based line, default 1), limit (lines, 1-200).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "workspace-relative file path"},
					"offset": map[string]any{"type": "integer", "description": "1-based start line"},
					"limit":  map[string]any{"type": "integer", "description": "max lines to read (1-200)"},
				},
				"required": []string{"path"},
			},
		},
	}
}

// invokeBuiltin dispatches a tool call to the builtin tool set.
func invokeBuiltin(name string, args map[string]any) (string, error) {
	switch name {
	case "read_file":
		return builtinReadFile(args)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// workspaceRoot is the directory the read_file tool confines itself to. In
// the beta it is the current working directory of the daemon; a later
// milestone makes it a per-session workspace State.
var workspaceRoot = func() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}()

const (
	builtinReadLimit   = 200
	builtinReadMaxLine = 1 << 20
)

// builtinReadFile reads a bounded page of a UTF-8 text file, confined to the
// workspace root (no path traversal).
func builtinReadFile(args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	if strings.TrimSpace(path) == "" {
		return "", errors.New("read_file: path is required")
	}
	offset := 1
	if v, ok := args["offset"].(float64); ok && v >= 1 {
		offset = int(v)
	}
	limit := builtinReadLimit
	if v, ok := args["limit"].(float64); ok && v >= 1 && v <= builtinReadLimit {
		limit = int(v)
	}
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", err
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, path)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("read_file: path %q is outside the workspace", path)
	}
	file, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("read_file: open: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), builtinReadMaxLine)
	lineNo := 0
	var out strings.Builder
	limitEnd := offset + limit
	for scanner.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		if lineNo >= limitEnd {
			break
		}
		fmt.Fprintf(&out, "%6d\t%s\n", lineNo, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read_file: scan: %w", err)
	}
	if out.Len() == 0 {
		return "read_file: no lines in range", nil
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
