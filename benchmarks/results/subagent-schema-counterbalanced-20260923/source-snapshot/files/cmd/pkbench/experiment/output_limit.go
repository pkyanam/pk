package experiment

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// Counters records the model's exact Bash argument choices before any default
// is applied. They are reported separately so an argument preview never has to
// be parsed to infer whether the model supplied a limit.
type Counters struct {
	explicit        atomic.Int64
	omitted         atomic.Int64
	defaulted       atomic.Int64
	outputBytes     atomic.Int64
	errorBytes      atomic.Int64
	rawOutputBytes  atomic.Int64
	rawErrorBytes   atomic.Int64
	outputTruncated atomic.Int64
	errorTruncated  atomic.Int64
	mu              sync.Mutex
	commands        map[string]string
	processed       map[operation.ID]struct{}
	failureLog      string
}

type Counts struct {
	Explicit        int64 `json:"explicit_bash_output_limits"`
	Omitted         int64 `json:"omitted_bash_output_limits"`
	Defaulted       int64 `json:"benchmark_default_applied"`
	OutputBytes     int64 `json:"bash_output_bytes"`
	ErrorBytes      int64 `json:"bash_error_bytes"`
	RawOutputBytes  int64 `json:"bash_raw_output_bytes"`
	RawErrorBytes   int64 `json:"bash_raw_error_bytes"`
	OutputTruncated int64 `json:"bash_output_truncated_operations"`
	ErrorTruncated  int64 `json:"bash_error_truncated_operations"`
}

func (c *Counters) Snapshot() Counts {
	return Counts{
		Explicit: c.explicit.Load(), Omitted: c.omitted.Load(), Defaulted: c.defaulted.Load(),
		OutputBytes: c.outputBytes.Load(), ErrorBytes: c.errorBytes.Load(),
		RawOutputBytes: c.rawOutputBytes.Load(), RawErrorBytes: c.rawErrorBytes.Load(),
		OutputTruncated: c.outputTruncated.Load(), ErrorTruncated: c.errorTruncated.Load(),
	}
}

// CaptureFailedCommands stores exact commands only for failed/nonzero Bash
// operations. Callers must place this in a private, mode-0700 result directory.
func (c *Counters) CaptureFailedCommands(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failureLog = path
	c.commands = make(map[string]string)
	c.processed = make(map[operation.ID]struct{})
}

// Decorate observes Bash limits in both arms. A zero limit is the unchanged
// runner; a positive limit is applied only when valid JSON object arguments
// omit max_output_length. Explicit values and malformed calls pass through.
func Decorate(base tool.Registry, limit int, counters *Counters) tool.Registry {
	if counters == nil {
		counters = &Counters{}
	}
	return registry{Registry: base, limit: limit, counters: counters}
}

type registry struct {
	tool.Registry
	limit    int
	counters *Counters
}

func (r registry) StaticDefinitions() []tool.Definition {
	definitions := r.Registry.StaticDefinitions()
	if r.limit <= 0 {
		return definitions
	}
	for i := range definitions {
		if definitions[i].Tool.Name != "Bash" {
			continue
		}
		properties, ok := definitions[i].Tool.Parameters["properties"].(map[string]any)
		if !ok {
			continue
		}
		limitSchema, ok := properties["max_output_length"].(map[string]any)
		if !ok {
			continue
		}
		limitSchema["default"] = r.limit
		limitSchema["description"] = fmt.Sprintf("Maximum characters per output text field. Truncated text keeps its head and tail, around a marker stating how much was omitted, and path to the file with the complete stream. Defaults to %d.", r.limit)
	}
	return definitions
}

func (r registry) Resolve(name string) (tool.Translator, bool) {
	translator, ok := r.Registry.Resolve(name)
	if !ok || name != "Bash" {
		return translator, ok
	}
	return bashTranslator{Translator: translator, limit: r.limit, counters: r.counters}, true
}

type bashTranslator struct {
	tool.Translator
	limit    int
	counters *Counters
}

func (t bashTranslator) Translate(ctx tool.Context, call llm.ToolCall) tool.CallStatus {
	var arguments map[string]json.RawMessage
	if json.Unmarshal([]byte(call.Arguments), &arguments) == nil && arguments != nil {
		var command string
		_ = json.Unmarshal(arguments["command"], &command)
		if command != "" {
			t.counters.mu.Lock()
			if t.counters.commands != nil {
				t.counters.commands[call.CallID] = command
			}
			t.counters.mu.Unlock()
		}
		if _, exists := arguments["max_output_length"]; exists {
			t.counters.explicit.Add(1)
		} else {
			t.counters.omitted.Add(1)
			if t.limit > 0 {
				arguments["max_output_length"], _ = json.Marshal(t.limit)
				if encoded, err := json.Marshal(arguments); err == nil {
					call.Arguments = string(encoded)
					t.counters.defaulted.Add(1)
				}
			}
		}
	}
	return t.Translator.Translate(ctx, call)
}

func (t bashTranslator) TranslateResult(callID string, status tool.CallStatus, operations []operation.Operation) (llm.ToolResult, error) {
	failed := status.Error != ""
	for _, current := range operations {
		if current.Status == operation.StatusFailed || current.Status == operation.StatusCanceled {
			failed = true
		}
		if current.Type == operation.TypeShell {
			if state, err := operation.DecodeShellState(current); err == nil {
				if state.Result != nil && state.Result.ExitCode != 0 {
					failed = true
				}
				if state.Result != nil || state.TerminalError != "" {
					t.counters.recordOperation(current.ID, state)
				}
			}
		}
	}
	if failed {
		t.counters.writeFailedCommand(callID)
	} else {
		t.counters.forgetCommand(callID)
	}
	return t.Translator.TranslateResult(callID, status, operations)
}

func (c *Counters) recordOperation(id operation.ID, state operation.ShellState) {
	c.mu.Lock()
	if c.processed == nil {
		c.processed = make(map[operation.ID]struct{})
	}
	if _, exists := c.processed[id]; exists {
		c.mu.Unlock()
		return
	}
	c.processed[id] = struct{}{}
	c.mu.Unlock()
	c.rawOutputBytes.Add(state.OutSize)
	c.rawErrorBytes.Add(state.ErrSize)
	if state.Result != nil {
		c.outputBytes.Add(int64(len(state.Result.Out)))
		c.errorBytes.Add(int64(len(state.Result.Err)))
	} else {
		c.errorBytes.Add(int64(len(state.TerminalError)))
	}
	if state.OutTruncated {
		c.outputTruncated.Add(1)
	}
	if state.ErrTruncated {
		c.errorTruncated.Add(1)
	}
}

func (c *Counters) forgetCommand(callID string) {
	c.mu.Lock()
	delete(c.commands, callID)
	c.mu.Unlock()
}

func (c *Counters) writeFailedCommand(callID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	command, ok := c.commands[callID]
	if !ok || c.failureLog == "" {
		return
	}
	delete(c.commands, callID)
	if err := os.MkdirAll(filepath.Dir(c.failureLog), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(c.failureLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_ = json.NewEncoder(file).Encode(map[string]string{"call_id": callID, "command": command})
	_ = file.Close()
}
