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
	"sync/atomic"
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
	features      []string
	lifecycle     chan LifecycleEvent
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
		return convertInto(InitializeResult{APIVersion: ProtocolVersion, ID: p.ID, Tools: w.tools, Commands: w.commands, Features: w.features}, result)
	case "lifecycle.notify":
		var event LifecycleEvent
		if err := convert(params, &event); err != nil {
			return err
		}
		if w.lifecycle != nil {
			select {
			case w.lifecycle <- event:
			default:
			}
		}
		return convertInto(struct{}{}, result)
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
	if len(report.Registered) != 2 || len(report.Loaded) != 0 || len(report.Disabled) != 0 {
		t.Fatalf("load report: %+v", report)
	}
	commands := host.SlashCommands()
	if len(commands) != 2 || commands[0].Name != "/ext:alpha:stats" || commands[1].Name != "/ext:beta:stats" || commands[0].Description != first.Commands[0].Description {
		t.Fatalf("slash catalog not deterministic or namespaced: %+v", commands)
	}
	if workers["alpha"] != nil || workers["beta"] != nil {
		t.Fatal("listing commands started an extension worker")
	}
	if _, err := host.ExecuteCommand(context.Background(), "stats", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous legacy command should be rejected, got %v", err)
	}
	result, err := host.ExecuteSlashCommand(context.Background(), "/ext:alpha:stats", "report now")
	if err != nil || result != "0 files, 0 directories, 0 bytes" {
		t.Fatalf("namespaced execution result=%q err=%v", result, err)
	}
	if workers["alpha"] == nil || workers["alpha"].commandCalls != 1 || workers["beta"] != nil || workers["alpha"].commandArgs[0] != "report now" {
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

func TestHostRegistersManifestWithoutStartingWorkerUntilToolUse(t *testing.T) {
	var starts atomic.Int32
	manifest := statsManifest("deferred", "deferred_tool")
	host, report, err := NewHost(context.Background(), t.TempDir(), []Manifest{manifest}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		starts.Add(1)
		return &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if starts.Load() != 0 || len(report.Registered) != 1 || len(report.Loaded) != 0 {
		t.Fatalf("registration started a worker: starts=%d report=%+v", starts.Load(), report)
	}
	if got := host.Tools(); len(got) != 1 || got[0].Name != "deferred_tool" {
		t.Fatalf("manifest tool schema was not registered lazily: %+v", got)
	}
	result, err := host.ExecuteTool(context.Background(), "deferred_tool", "call-1", json.RawMessage(`{}`))
	if err != nil || len(result.Content) != 1 || starts.Load() != 1 {
		t.Fatalf("first tool call result=%+v err=%v starts=%d", result, err, starts.Load())
	}
}

func TestConcurrentFirstToolCallsShareOneWorkerInitialization(t *testing.T) {
	var starts atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	manifest := statsManifest("concurrent", "concurrent_tool")
	host, _, err := NewHost(context.Background(), t.TempDir(), []Manifest{manifest}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		if starts.Add(1) == 1 {
			close(started)
		}
		<-release
		return &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	results := make(chan error, 2)
	for _, id := range []string{"call-a", "call-b"} {
		go func(callID string) {
			_, err := host.ExecuteTool(context.Background(), "concurrent_tool", callID, json.RawMessage(`{}`))
			results <- err
		}(id)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first tool call did not start worker initialization")
	}
	close(release)
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent tool call did not complete")
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("concurrent first calls started %d workers", starts.Load())
	}
}

func TestInitializationTimeoutReturnsOnceAndRetriesOnlyOnNextInvocation(t *testing.T) {
	var starts atomic.Int32
	manifest := statsManifest("init-timeout", "init_timeout_tool")
	host, _, err := NewHost(context.Background(), t.TempDir(), []Manifest{manifest}, func(context.Context, Manifest, string) (Worker, error) {
		starts.Add(1)
		return nil, context.DeadlineExceeded
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	for expected := int32(1); expected <= 2; expected++ {
		_, err := host.ExecuteTool(context.Background(), "init_timeout_tool", fmt.Sprintf("call-%d", expected), json.RawMessage(`{}`))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("initialization attempt %d returned %v", expected, err)
		}
		if got := starts.Load(); got != expected {
			t.Fatalf("factory started %d times after explicit attempt %d", got, expected)
		}
	}
}

type cancelInitWorker struct{ closed atomic.Bool }

func (w *cancelInitWorker) Call(ctx context.Context, method string, _ any, _ any) error {
	if method == "initialize" {
		<-ctx.Done()
		return ctx.Err()
	}
	return errors.New("unexpected call")
}
func (w *cancelInitWorker) Close() error { w.closed.Store(true); return nil }

func TestHostCloseCancelsAndClosesWorkerDuringLazyInitialization(t *testing.T) {
	manifest := statsManifest("cancel-init", "cancel_init_tool")
	workerCreated := make(chan *cancelInitWorker, 1)
	host, _, err := NewHost(context.Background(), t.TempDir(), []Manifest{manifest}, func(context.Context, Manifest, string) (Worker, error) {
		worker := &cancelInitWorker{}
		workerCreated <- worker
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	callCtx, cancelCall := context.WithCancel(context.Background())
	go func() {
		_, err := host.ExecuteTool(callCtx, "cancel_init_tool", "call", json.RawMessage(`{}`))
		callDone <- err
	}()
	var worker *cancelInitWorker
	select {
	case worker = <-workerCreated:
	case <-time.After(time.Second):
		t.Fatal("worker was not constructed")
	}
	cancelCall()
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled first invocation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled first invocation remained blocked on initialization")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if !worker.closed.Load() {
		t.Fatal("worker created during startup was not closed")
	}
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
	if len(report.Registered) != 1 || report.Registered[0] != "stats-extension" || len(report.Loaded) != 0 || len(report.Disabled) != 0 {
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
	if len(report.Registered) != 2 || report.Registered[0] != "first" || len(report.Loaded) != 0 || len(report.Disabled) != 1 {
		t.Fatalf("report: %+v", report)
	}
	if len(host.Tools()) != 2 || host.Tools()[1].Name != "shared_tool" {
		t.Fatalf("conflict broke valid extension: %+v", host.Tools())
	}
	if _, err := host.ExecuteTool(context.Background(), "bad_tool", "call-bad", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("deferred worker failure was not reported at invocation: %v", err)
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
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported lifecycle event") {
		t.Fatalf("unsupported hook validation: %v", err)
	}
	manifest = statsManifest("stats-extension", "workspace_stats")
	manifest.Hooks = []HookSpec{{Event: LifecycleRunStart, Mode: "transform"}}
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "mode observe") {
		t.Fatalf("transform hook validation: %v", err)
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

func TestLifecycleNotificationsRequireWorkerOptInAndDeliverMetadata(t *testing.T) {
	workspace := t.TempDir()
	manifest := statsManifest("observer", "observer_tool")
	manifest.Hooks = []HookSpec{{Event: LifecycleRunStart, Mode: "observe"}, {Event: LifecycleResponseComplete, Mode: "observe"}, {Event: LifecycleRunEnd, Mode: "observe"}}
	var worker *fakeWorker
	host, report, err := NewHost(context.Background(), workspace, []Manifest{manifest}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		worker = &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands), features: []string{HostFeatureLifecycle}, lifecycle: make(chan LifecycleEvent, 8)}
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || report.Loaded[0] != "observer" || len(report.Disabled) != 0 {
		t.Fatalf("load report: %+v", report)
	}
	event := LifecycleEvent{Type: LifecycleRunStart, RunID: "run-1", SessionID: "session-1", Model: "gpt-6-luna", Workspace: "/untrusted/override", Status: "started"}
	if !host.NotifyLifecycle(event) {
		t.Fatal("opted-in observer did not accept event")
	}
	select {
	case got := <-worker.lifecycle:
		if got.Type != event.Type || got.RunID != event.RunID || got.SessionID != event.SessionID || got.Model != event.Model || got.Status != event.Status || got.Workspace != workspace {
			t.Fatalf("lifecycle event=%+v", got)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "prompt") || strings.Contains(string(encoded), "transcript") || strings.Contains(string(encoded), "credential") {
			t.Fatalf("sensitive lifecycle payload: %s", encoded)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle event was not delivered")
	}
	if host.NotifyLifecycle(LifecycleEvent{Type: "session_start"}) {
		t.Fatal("unsupported event was accepted")
	}
}

func TestLifecycleManifestRequiresWorkerOptIn(t *testing.T) {
	manifest := statsManifest("observer", "observer_tool")
	manifest.Hooks = []HookSpec{{Event: LifecycleRunEnd, Mode: "observe"}}
	host, report, err := NewHost(context.Background(), t.TempDir(), []Manifest{manifest}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		return &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	if len(report.Loaded) != 0 || len(report.Disabled) != 1 || !strings.Contains(report.Disabled[0].Error(), "did not opt in") {
		t.Fatalf("missing opt-in report: %+v", report)
	}
	if host.NotifyLifecycle(LifecycleEvent{Type: LifecycleRunEnd}) {
		t.Fatal("notification accepted without an observer")
	}
}

type stalledLifecycleWorker struct {
	*fakeWorker
	started chan struct{}
	release chan struct{}
}

func (w *stalledLifecycleWorker) Call(ctx context.Context, method string, params any, result any) error {
	if method != "lifecycle.notify" {
		return w.fakeWorker.Call(ctx, method, params, result)
	}
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return nil
}

func (w *stalledLifecycleWorker) Close() error {
	select {
	case <-w.release:
	default:
		close(w.release)
	}
	return w.fakeWorker.Close()
}

func TestLifecycleQueueIsNonblockingBoundedAndCloseIsBounded(t *testing.T) {
	manifest := statsManifest("observer", "observer_tool")
	manifest.Hooks = []HookSpec{{Event: LifecycleRunEnd, Mode: "observe"}}
	var worker *stalledLifecycleWorker
	host, report, err := NewHost(context.Background(), t.TempDir(), []Manifest{manifest}, func(_ context.Context, m Manifest, root string) (Worker, error) {
		worker = &stalledLifecycleWorker{fakeWorker: &fakeWorker{id: m.ID, workspace: root, tools: namesOfTools(m.Tools), commands: namesOfCommands(m.Commands), features: []string{HostFeatureLifecycle}}, started: make(chan struct{}, 1), release: make(chan struct{})}
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Loaded) != 1 || len(report.Registered) != 1 {
		t.Fatalf("load report: %+v", report)
	}
	start := time.Now()
	dropped := false
	for i := 0; i < MaxLifecycleQueue+8; i++ {
		if !host.NotifyLifecycle(LifecycleEvent{Type: LifecycleRunEnd, RunID: fmt.Sprintf("run-%d", i)}) {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("full observer queue did not drop an event")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("enqueue blocked for %s", elapsed)
	}
	select {
	case <-worker.started:
	case <-time.After(time.Second):
		t.Fatal("observer did not receive first event")
	}
	closeStart := time.Now()
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(closeStart); elapsed > lifecycleCloseDrain+time.Second {
		t.Fatalf("host close took %s", elapsed)
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
	if len(report.Loaded) != 0 || len(report.Registered) != 2 {
		t.Fatalf("report: %+v", report)
	}
	fingerprint := host.SchemaFingerprint()
	if err := host.SetCallTimeout(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := host.ExecuteTool(context.Background(), "blocked_tool", "call", json.RawMessage(`{}`)); err == nil {
		t.Fatal("blocked worker did not time out")
	}
	if _, err := host.ensureWorker(context.Background(), host.tools["blocked_tool"]); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled cached worker was returned after timeout: %v", err)
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
	if len(report.Loaded) != 0 || len(report.Registered) != 1 {
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
