package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type fakeWorker struct {
	id            string
	workspace     string
	closed        bool
	tools         []string
	commands      []string
	commandCalls  int
	commandArgs   []string
	commandResult string
}

func (w *fakeWorker) Call(ctx context.Context, method string, params any, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	switch method {
	case "initialize":
		var p InitializeParams
		if err := convert(params, &p); err != nil {
			return err
		}
		return convertInto(InitializeResult{APIVersion: ProtocolVersion, ID: p.ID, Tools: w.tools, Commands: w.commands}, result)
	case "tool.execute":
		var p ToolExecuteParams
		if err := convert(params, &p); err != nil {
			return err
		}
		stats, err := countWorkspace(p.Workspace)
		if err != nil {
			return err
		}
		return convertInto(ToolResult{Content: []Content{{Type: "text", Text: formatStats(stats)}}, Details: rawJSON(stats)}, result)
	case "command.execute":
		var p CommandExecuteParams
		if err := convert(params, &p); err != nil {
			return err
		}
		w.commandCalls++
		w.commandArgs = append(w.commandArgs, p.Arguments)
		if w.commandResult != "" {
			return convertInto(w.commandResult, result)
		}
		stats, err := countWorkspace(p.Workspace)
		if err != nil {
			return err
		}
		return convertInto(formatStats(stats), result)
	default:
		return &RPCError{Code: "method_not_found", Message: method}
	}
}

func TestSlashCommandCatalogIsNamespacedAndExecutionIsExplicit(t *testing.T) {
	workspace := t.TempDir()
	first, second := statsManifest("alpha", "alpha_tool"), statsManifest("beta", "beta_tool")
	var workers = map[string]*fakeWorker{}
	host, report, err := NewHost(context.Background(), workspace, []Manifest{second, first}, func(_ context.Context, manifest Manifest, root string) (Worker, error) {
		worker := &fakeWorker{id: manifest.ID, workspace: root, tools: namesOfTools(manifest.Tools), commands: namesOfCommands(manifest.Commands)}
		workers[manifest.ID] = worker
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 2 || len(report.Disabled) != 0 {
		t.Fatalf("load report: %+v", report)
	}
	commands := host.SlashCommands()
	if len(commands) != 2 || commands[0].Name != "/ext:alpha:stats" || commands[1].Name != "/ext:beta:stats" || commands[0].Description != first.Commands[0].Description {
		t.Fatalf("slash catalog not deterministic or namespaced: %+v", commands)
	}
	if workers["alpha"].commandCalls != 0 || workers["beta"].commandCalls != 0 {
		t.Fatal("listing commands invoked an extension command")
	}
	if _, err := host.ExecuteCommand(context.Background(), "stats", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous legacy command should be rejected, got %v", err)
	}
	result, err := host.ExecuteSlashCommand(context.Background(), "/ext:alpha:stats", "report now")
	if err != nil || result != "0 files, 0 directories, 0 bytes" {
		t.Fatalf("namespaced execution result=%q err=%v", result, err)
	}
	if workers["alpha"].commandCalls != 1 || workers["beta"].commandCalls != 0 || workers["alpha"].commandArgs[0] != "report now" {
		t.Fatalf("execution routed incorrectly: alpha=%+v beta=%+v", workers["alpha"], workers["beta"])
	}
}

func TestSlashCommandBoundsArgumentsAndResults(t *testing.T) {
	manifest := statsManifest("bounded", "bounded_tool")
	var worker *fakeWorker
	host, _, err := NewHost(context.Background(), t.TempDir(), []Manifest{manifest}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		worker = &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	name := "/ext:bounded:stats"
	if _, err := host.ExecuteSlashCommand(context.Background(), name, strings.Repeat("x", MaxSlashCommandArgumentBytes+1)); err == nil || !strings.Contains(err.Error(), "arguments exceed") {
		t.Fatalf("oversize command args error=%v", err)
	}
	if _, err := host.ExecuteSlashCommand(context.Background(), name, string([]byte{0xff})); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 command args error=%v", err)
	}
	if worker.commandCalls != 0 {
		t.Fatal("invalid command arguments reached worker")
	}
	worker.commandResult = strings.Repeat("y", MaxSlashCommandResultBytes+1)
	if _, err := host.ExecuteSlashCommand(context.Background(), name, ""); err == nil || !strings.Contains(err.Error(), "result exceeds") {
		t.Fatalf("oversize command result error=%v", err)
	}
}

func (w *fakeWorker) Close() error { w.closed = true; return nil }

func convert(from, to any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, to)
}
func convertInto(from, to any) error    { return convert(from, to) }
func rawJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

func statsManifest(id, toolName string) Manifest {
	return Manifest{APIVersion: ProtocolVersion, ID: id, Version: "1.0.0", Executable: "fake-worker", Capabilities: []string{"workspace.read"},
		Tools:    []ToolSpec{{Name: toolName, Description: "Count workspace files", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}},
		Commands: []CommandSpec{{Name: "stats", Description: "Count workspace files"}}}
}

func TestHostLoadsWorkspaceStatsToolAndCommand(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sub", "data.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	var worker *fakeWorker
	host, report, err := NewHost(context.Background(), workspace, []Manifest{statsManifest("stats-extension", "workspace_stats")}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		worker = &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || report.Loaded[0] != "stats-extension" || len(report.Disabled) != 0 {
		t.Fatalf("load report: %+v", report)
	}
	result, err := host.ExecuteTool(context.Background(), "workspace_stats", "call-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "1 files, 1 directories, 3 bytes" {
		t.Fatalf("tool result: %+v", result)
	}
	command, err := host.ExecuteCommand(context.Background(), "stats", "")
	if err != nil || command != result.Content[0].Text {
		t.Fatalf("command result %q, %v", command, err)
	}
	if len(host.SchemaFingerprint()) != 64 {
		t.Fatalf("bad schema fingerprint %q", host.SchemaFingerprint())
	}
	if worker == nil || worker.closed {
		t.Fatal("worker lifecycle unexpected before Close")
	}
}

type fakeToolContext struct{ spec operation.Spec }

func (c *fakeToolContext) Submit(spec operation.Spec) operation.ID { c.spec = spec; return "result-1" }

func TestRegistryDecoratorUsesAsyncRemoteJobAndBuiltinsWin(t *testing.T) {
	host, _, err := NewHost(context.Background(), t.TempDir(), []Manifest{statsManifest("stats-extension", tool.BashName)}, func(_ context.Context, m Manifest, _ string) (Worker, error) {
		return &fakeWorker{tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	base := tool.NewRegistry(tool.StaticTranslators{}, tool.BashName)
	decorated, issues := DecorateRegistry(base, host)
	if len(issues) != 1 {
		t.Fatalf("conflict diagnostics: %v", issues)
	}
	if _, ok := decorated.Resolve(tool.BashName); !ok {
		t.Fatal("extension replaced built-in bash")
	}
	if len(host.Tools()) != 1 {
		t.Fatal("runtime tool declarations must remain frozen after conflict")
	}
	if _, err := host.ExecuteCommand(context.Background(), "stats", ""); err == nil {
		t.Fatal("disabled extension command still available")
	}

	host2, _, err := NewHost(context.Background(), t.TempDir(), []Manifest{statsManifest("stats-extension", "workspace_stats")}, func(_ context.Context, m Manifest, _ string) (Worker, error) {
		return &fakeWorker{tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host2.Close()
	decorated, issues = DecorateRegistry(tool.NewRegistry(tool.StaticTranslators{}), host2)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	translator, ok := decorated.Resolve("workspace_stats")
	if !ok {
		t.Fatal("extension tool was not registered")
	}
	ctx := &fakeToolContext{}
	status := translator.Translate(ctx, llm.ToolCall{CallID: "tool-call", Name: "workspace_stats", Arguments: `{}`})
	if status.Error != "" || len(status.WaitingFor) != 1 || ctx.spec.Type != operation.TypeRemoteJob {
		t.Fatalf("tool did not use durable operation: %+v %+v", status, ctx.spec)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	handler := newRemoteHandler(runCtx, host2)
	op := operation.Operation{ID: status.WaitingFor[0], Type: ctx.spec.Type, Version: ctx.spec.Version, State: ctx.spec.State, Status: operation.StatusReady, MaxOutputLength: ctx.spec.MaxOutputLength}
	interim, err := translator.TranslateResult("tool-call", status, []operation.Operation{op})
	if err != nil || len(interim.Output) != 1 || interim.Output[0].Value != "Extension tool is running." {
		cancel()
		t.Fatalf("nonterminal operation result: %#v, %v", interim, err)
	}
	if err := handler.AddRemoteJob(op); err != nil {
		cancel()
		t.Fatal(err)
	}
	var terminal operation.Operation
	deadline := time.After(time.Second)
	for terminal.Status != operation.StatusCompleted {
		select {
		case terminal = <-handler.RemoteJobUpdates():
		case <-deadline:
			cancel()
			t.Fatal("extension operation did not complete")
		}
	}
	cancel()
	handler.Wait()
	result, err := translator.TranslateResult("tool-call", status, []operation.Operation{terminal})
	if err != nil || len(result.Output) != 1 || !strings.Contains(result.Output[0].Value, "0 bytes") {
		t.Fatalf("translated result %#v, %v", result, err)
	}
}

func TestHostIsolatesFailedAndConflictingExtensions(t *testing.T) {
	workspace := t.TempDir()
	first := statsManifest("first", "shared_tool")
	second := statsManifest("second", "shared_tool")
	bad := statsManifest("bad", "bad_tool")
	var workers []*fakeWorker
	host, report, err := NewHost(context.Background(), workspace, []Manifest{first, second, bad}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		if m.ID == "bad" {
			return nil, errors.New("cannot start")
		}
		worker := &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}
		workers = append(workers, worker)
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || report.Loaded[0] != "first" || len(report.Disabled) != 2 {
		t.Fatalf("report: %+v", report)
	}
	if len(host.Tools()) != 1 || host.Tools()[0].Name != "shared_tool" {
		t.Fatalf("conflict broke valid extension: %+v", host.Tools())
	}
}

func TestManifestRejectsUnsupportedDeclarationsAndRelativeExecutableIsStable(t *testing.T) {
	manifest := statsManifest("stats-extension", "workspace_stats")
	manifest.Executable = ""
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "no executable") {
		t.Fatalf("empty executable validation: %v", err)
	}
	manifest = statsManifest("stats-extension", "workspace_stats")
	manifest.Version = "latest"
	if err := manifest.Validate(); err == nil {
		t.Fatal("non-semver version accepted")
	}
	manifest = statsManifest("stats-extension", "workspace_stats")
	manifest.Hooks = []HookSpec{{Event: "agent_start", Mode: "observe"}}
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported hook validation: %v", err)
	}

	path := filepath.Join(t.TempDir(), "manifest.json")
	manifest = statsManifest("stats-extension", "workspace_stats")
	manifest.Executable = "./worker"
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Executable != filepath.Join(filepath.Dir(path), "worker") {
		t.Fatalf("executable path = %q", loaded.Executable)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), "pipe"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRequiresBoundedCommandDescriptions(t *testing.T) {
	manifest := statsManifest("stats-extension", "workspace_stats")
	manifest.Commands[0].Description = "  "
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "no description") {
		t.Fatalf("empty command description validation error=%v", err)
	}
	manifest = statsManifest("stats-extension", "workspace_stats")
	manifest.Commands[0].Description = strings.Repeat("d", maxCommandDescriptionBytes+1)
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "description exceeds") {
		t.Fatalf("oversize command description validation error=%v", err)
	}
}

func countWorkspace(root string) (stats, error) {
	var result stats
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
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

type stats struct {
	Files       int64 `json:"files"`
	Directories int64 `json:"directories"`
	Bytes       int64 `json:"bytes"`
}

func formatStats(s stats) string {
	return fmt.Sprintf("%d files, %d directories, %d bytes", s.Files, s.Directories, s.Bytes)
}

func TestHostTimeoutDisablesOnlyItsWorker(t *testing.T) {
	workspace := t.TempDir()
	blocked := &blockingWorker{}
	manifests := []Manifest{statsManifest("blocked", "blocked_tool"), statsManifest("healthy", "healthy_tool")}
	manifests[1].Commands[0].Name = "healthy_stats"
	host, report, err := NewHost(context.Background(), workspace, manifests, func(_ context.Context, m Manifest, _ string) (Worker, error) {
		if m.ID == "blocked" {
			blocked.tools = namesOfTools(m.Tools)
			blocked.commands = namesOfCommands(m.Commands)
			return blocked, nil
		}
		return &fakeWorker{tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 2 {
		t.Fatalf("report: %+v", report)
	}
	fingerprint := host.SchemaFingerprint()
	if err := host.SetCallTimeout(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := host.ExecuteTool(context.Background(), "blocked_tool", "call", json.RawMessage(`{}`)); err == nil {
		t.Fatal("blocked worker did not time out")
	}
	if len(host.Tools()) != 2 || host.SchemaFingerprint() != fingerprint {
		t.Fatalf("runtime failure changed frozen schema: %+v", host.Tools())
	}
}

type blockingWorker struct {
	closed          bool
	tools, commands []string
}

type progressFakeWorker struct{ fakeWorker }

func (w *progressFakeWorker) Call(ctx context.Context, method string, params any, result any) error {
	if method == "tool.execute" {
		p := params.(ToolExecuteParams)
		if !ReportProgress(ctx, "working") {
			return errors.New("progress was not enabled")
		}
		return convertInto(ToolResult{Content: []Content{{Type: "text", Text: p.CallID}}}, result)
	}
	return w.fakeWorker.Call(ctx, method, params, result)
}

func TestHostDeliversProgressWithModelToolCallID(t *testing.T) {
	workspace := t.TempDir()
	manifest := statsManifest("progress", "progress_tool")
	host, report, err := NewHost(context.Background(), workspace, []Manifest{manifest}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		return &progressFakeWorker{fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 {
		t.Fatalf("load report: %+v", report)
	}
	got := make(chan ProgressEvent, 1)
	host.SetProgressHandler(func(event ProgressEvent) { got <- event })
	result, err := host.ExecuteTool(context.Background(), "progress_tool", "model-call-42", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "model-call-42" {
		t.Fatalf("result=%+v", result)
	}
	select {
	case event := <-got:
		if event.ExtensionID != "progress" || event.ToolName != "progress_tool" || event.CallID != "model-call-42" || event.Text != "working" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("progress callback was not delivered")
	}
}

func (w *blockingWorker) Call(ctx context.Context, method string, params any, result any) error {
	if method == "initialize" {
		var p InitializeParams
		_ = convert(params, &p)
		return convertInto(InitializeResult{APIVersion: ProtocolVersion, ID: p.ID, Tools: w.tools, Commands: w.commands}, result)
	}
	<-ctx.Done()
	return ctx.Err()
}
func (w *blockingWorker) Close() error { w.closed = true; return nil }
