package integration

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pkyanam/pk/internal/imagegen"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

type scriptedAdapter struct {
	mu        sync.Mutex
	responses []llm.Response
	requests  []llm.Request
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func (a *scriptedAdapter) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, request)
	if len(a.responses) == 0 {
		return llm.Response{}, io.ErrUnexpectedEOF
	}
	response := a.responses[0]
	a.responses = a.responses[1:]
	return response, nil
}

func TestRunWaitsForAsyncBashAndEmitsFinalAssistantText(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	args, err := json.Marshal(map[string]string{"command": "printf async-result"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{
		{ID: "tool-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-1", Name: "Bash", Arguments: string(args)}}}},
		{ID: "final-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "finished"}}}},
	}}
	var output strings.Builder
	result, err := runner.Run(ctx, runner.Options{
		Prompt: "run the command", SessionDir: t.TempDir(), Workspace: t.TempDir(),
		Adapter: adapter, Output: &output,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("Run() returned an empty session ID")
	}
	if result.Text != "finished\n" {
		t.Errorf("Run().Text = %q, want final assistant text", result.Text)
	}
	if got := output.String(); !strings.Contains(got, "finished") {
		t.Errorf("output %q does not include final assistant text", got)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 2 {
		t.Fatalf("LLM requests = %d, want tool call then final response", len(adapter.requests))
	}
	foundResult := false
	for _, item := range adapter.requests[1].Input {
		if item.Type == llm.ItemToolResult {
			result, ok := item.Data.(llm.ToolResult)
			if !ok {
				continue
			}
			for _, output := range result.Output {
				if strings.Contains(output.Value, "async-result") {
					foundResult = true
				}
			}
		}
	}
	if !foundResult {
		t.Error("second LLM request did not include the asynchronous Bash output")
	}
}

func TestRunDispatchesIndependentToolsConcurrentlyAndWaitsForBoth(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	workspace := t.TempDir()
	marker := workspace + "/fast-finished"
	started := workspace + "/waiter-started"
	fastArgs, err := json.Marshal(map[string]string{"command": "i=0; while [ ! -f '" + started + "' ] && [ $i -lt 200 ]; do sleep 0.01; i=$((i+1)); done; test -f '" + started + "' && printf fast-ok > '" + marker + "' && printf fast-ok"})
	if err != nil {
		t.Fatal(err)
	}
	slowArgs, err := json.Marshal(map[string]string{"command": "printf started > '" + started + "'; i=0; while [ ! -f '" + marker + "' ] && [ $i -lt 200 ]; do sleep 0.01; i=$((i+1)); done; test -f '" + marker + "' && printf slow-ok"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{
		{ID: "parallel-tools", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "slow-call", Name: "Bash", Arguments: string(slowArgs)}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "fast-call", Name: "Bash", Arguments: string(fastArgs)}},
		}},
		{ID: "parallel-final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "both completed"}}}},
	}}
	_, err = runner.Run(ctx, runner.Options{Prompt: "run both commands", SessionDir: t.TempDir(), Workspace: workspace, Adapter: adapter})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fast command did not run: %v", err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 2 {
		t.Fatalf("LLM requests = %d, want initial and post-tools", len(adapter.requests))
	}
	got := map[string]string{}
	for _, item := range adapter.requests[1].Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		result, ok := item.Data.(llm.ToolResult)
		if !ok {
			continue
		}
		var text strings.Builder
		for _, output := range result.Output {
			text.WriteString(output.Value)
		}
		got[result.CallID] = text.String()
	}
	if !strings.Contains(got["fast-call"], "fast-ok") || !strings.Contains(got["slow-call"], "slow-ok") {
		t.Errorf("post-tool results = %#v, want both completed command outputs", got)
	}
}

func TestRunDeliversRunningPlaceholderThenFinalCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	workspace := t.TempDir()
	started := workspace + "/waiter-started"
	producerDone := workspace + "/producer-done"
	waiterArgs, err := json.Marshal(map[string]string{"command": "printf started > '" + started + "'; i=0; while [ ! -f '" + producerDone + "' ] && [ $i -lt 200 ]; do sleep 0.01; i=$((i+1)); done; test -f '" + producerDone + "' && sleep 1.5 && printf waiter-final"})
	if err != nil {
		t.Fatal(err)
	}
	producerArgs, err := json.Marshal(map[string]string{"command": "i=0; while [ ! -f '" + started + "' ] && [ $i -lt 200 ]; do sleep 0.01; i=$((i+1)); done; test -f '" + started + "' && printf producer-final > '" + producerDone + "' && printf producer-final"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{
		{ID: "start-tools", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "waiter", Name: "Bash", Arguments: string(waiterArgs)}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "producer", Name: "Bash", Arguments: string(producerArgs)}},
		}},
		{ID: "waiting-turn", Stop: llm.StopComplete},
		{ID: "completed-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "waiter finished"}}}},
	}}
	result, err := runner.Run(ctx, runner.Options{Prompt: "run both and report final output", SessionDir: t.TempDir(), Workspace: workspace, Adapter: adapter})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Text != "waiter finished\n" {
		t.Errorf("Run().Text = %q, want final response after the slow tool", result.Text)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 3 {
		t.Fatalf("LLM requests = %d, want initial, still-running, and completed turns", len(adapter.requests))
	}
	results := func(request llm.Request) map[string]string {
		got := make(map[string]string)
		for _, item := range request.Input {
			if item.Type != llm.ItemToolResult {
				continue
			}
			result, ok := item.Data.(llm.ToolResult)
			if !ok {
				continue
			}
			var text strings.Builder
			for _, output := range result.Output {
				text.WriteString(output.Value)
			}
			got[result.CallID] = text.String()
		}
		return got
	}
	interim := results(adapter.requests[1])
	if !strings.Contains(interim["producer"], "producer-final") || !strings.Contains(interim["waiter"], "still running") {
		t.Errorf("interim results = %#v, want producer output and waiter running placeholder", interim)
	}
	final := results(adapter.requests[2])
	if !strings.Contains(final["waiter"], "waiter-final") {
		t.Errorf("final tool results = %#v, want completed waiter output", final)
	}
}

func TestRunResumesThePersistedConversation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sessionDir, workspace := t.TempDir(), t.TempDir()
	first := &scriptedAdapter{responses: []llm.Response{{
		ID: "first-response", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "first answer"}}},
	}}}
	created, err := runner.Run(ctx, runner.Options{Prompt: "first question", SessionDir: sessionDir, Workspace: workspace, Adapter: first})
	if err != nil {
		t.Fatalf("initial Run() error = %v", err)
	}
	second := &scriptedAdapter{responses: []llm.Response{{
		ID: "second-response", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "second answer"}}},
	}}}
	resumed, err := runner.Run(ctx, runner.Options{Prompt: "follow-up", SessionID: created.SessionID, SessionDir: sessionDir, Workspace: workspace, Adapter: second})
	if err != nil {
		t.Fatalf("resumed Run() error = %v", err)
	}
	if resumed.SessionID != created.SessionID {
		t.Fatalf("resumed session ID = %q, want %q", resumed.SessionID, created.SessionID)
	}
	if resumed.Text != "second answer\n" {
		t.Errorf("resumed text = %q, want second answer", resumed.Text)
	}
	if len(second.requests) != 1 {
		t.Fatalf("resumed LLM requests = %d, want 1", len(second.requests))
	}
	foundPriorAnswer := false
	for _, item := range second.requests[0].Input {
		if item.Type != llm.ItemMessage {
			continue
		}
		message, ok := item.Data.(llm.Message)
		if ok && message.Text == "first answer" {
			foundPriorAnswer = true
			break
		}
	}
	if !foundPriorAnswer {
		t.Error("resumed LLM request omitted the persisted first answer")
	}
}

func TestRunReturnsAssistantOutputWriteFailure(t *testing.T) {
	want := fmt.Errorf("output unavailable")
	adapter := &scriptedAdapter{responses: []llm.Response{{
		ID: "write-failure", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "hello"}}},
	}}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := runner.Run(ctx, runner.Options{
		Prompt: "answer", SessionDir: t.TempDir(), Workspace: t.TempDir(), Adapter: adapter,
		Output: failingWriter{err: want},
	})
	if !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want output writer error %v", err, want)
	}
}

type cancelAwareAdapter struct{ called chan struct{} }

func (a cancelAwareAdapter) Respond(ctx context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	select {
	case a.called <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

type providerFailureAfterGrace struct {
	mu    sync.Mutex
	calls int
	first llm.Response
	err   error
}

func (a *providerFailureAfterGrace) Respond(_ context.Context, _ llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.calls == 1 {
		return a.first, nil
	}
	return llm.Response{}, a.err
}

func TestRunCancellationSettlesModelRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	called := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{
			Prompt: "wait", SessionDir: t.TempDir(), Workspace: t.TempDir(),
			Adapter: cancelAwareAdapter{called: called},
		})
		done <- err
	}()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("adapter was not called")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error after cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not settle after caller cancellation")
	}
}

func TestRunCancellationKillsActiveBashProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workspace := t.TempDir()
	marker := workspace + "/child.pid"
	args, err := json.Marshal(map[string]string{"command": "printf '%s' $$ > '" + marker + "'; exec sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{{
		ID: "tool-turn", Stop: llm.StopComplete,
		Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "sleep-call", Name: "Bash", Arguments: string(args)}}},
	}}}
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{Prompt: "start a process", SessionDir: t.TempDir(), Workspace: workspace, Adapter: adapter})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(marker)
		if readErr == nil {
			pid, _ = strconv.Atoi(string(data))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		cancel()
		t.Fatal("Bash child did not write its PID")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error after cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not settle after canceling active Bash")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("Bash process %d survived Run cancellation", pid)
}

func TestRunImageGenReturnsArtifactToModel(t *testing.T) {
	workspace, sessionDir, codexHome := t.TempDir(), t.TempDir(), t.TempDir()
	thread := "01a0cc58-0b27-7c20-8f9b-3efe81d0553b"
	executable := filepath.Join(t.TempDir(), "codex-fixture")
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(generated, pngData.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nmkdir -p \"$CODEX_HOME/generated_images/" + thread + "\"\ncp '" + generated + "' \"$CODEX_HOME/generated_images/" + thread + "/exec-one.png\"\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	responses := []llm.Response{
		{ID: "imagegen-tool", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "image-call", Name: imagegen.ToolName, Arguments: `{"prompt":"mint leaf","output_path":"art/leaf.png"}`}}}},
		{ID: "after-imagegen", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "image saved"}}}},
	}
	adapter := &scriptedAdapter{responses: responses}
	config := imagegen.Config{Executable: executable, CodexHome: codexHome, Driver: "gpt-6-astra", Timeout: time.Minute}
	result, err := runner.Run(t.Context(), runner.Options{
		Prompt: "draw a leaf", SessionDir: sessionDir, Workspace: workspace, Adapter: adapter,
		DecorateRegistry: imagegen.Decorator(config, workspace), RemoteJobHandlers: imagegen.HandlerFactory(config, workspace),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "image saved" {
		t.Fatalf("final response = %q", result.Text)
	}
	artifact := filepath.Join(workspace, "art", "leaf.png")
	if info, err := os.Stat(artifact); err != nil || info.Size() != int64(pngData.Len()) {
		t.Fatalf("artifact stat = %v, %v", info, err)
	}
	artifact, err = filepath.EvalSymlinks(artifact)
	if err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 2 {
		t.Fatalf("provider requests = %d, want tool result follow-up", len(adapter.requests))
	}
	var resultText string
	for _, item := range adapter.requests[1].Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		toolResult := item.Data.(llm.ToolResult)
		if toolResult.CallID != "image-call" {
			continue
		}
		for _, output := range toolResult.Output {
			resultText += output.Value
		}
	}
	if !strings.Contains(resultText, `"path":"`+artifact+`"`) || !strings.Contains(resultText, `"width":3`) || !strings.Contains(resultText, `"height":2`) {
		t.Fatalf("model received no artifact metadata: %q", resultText)
	}
}

func TestRunCancellationSettlesImageGenRemoteJobAndKillsWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell and process IDs")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workspace, sessionDir, codexHome := t.TempDir(), t.TempDir(), t.TempDir()
	store, err := localfile.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "image-worker.pid")
	thread := "01a0cc58-0b27-7c20-8f9b-3efe81d0553b"
	executable := filepath.Join(t.TempDir(), "slow-codex")
	script := "#!/bin/sh\nprintf '%s' $$ > '" + marker + "'\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\nexec sleep 30\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	args := `{"prompt":"mint leaf","output_path":"leaf.png"}`
	adapter := &scriptedAdapter{responses: []llm.Response{{ID: "imagegen-call", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "image-call", Name: imagegen.ToolName, Arguments: args}}}}}}
	imageConfig := imagegen.Config{Executable: executable, CodexHome: codexHome, Driver: "gpt-6-astra", Timeout: time.Minute}
	done := make(chan error, 1)
	var sessionID string
	go func() {
		_, err := runner.Run(ctx, runner.Options{
			Prompt: "create a leaf", SessionDir: sessionDir, Workspace: workspace, Adapter: adapter,
			Store: store, OnSession: func(id string) { sessionID = id },
			DecorateRegistry:  imagegen.Decorator(imageConfig, workspace),
			RemoteJobHandlers: imagegen.HandlerFactory(imageConfig, workspace),
		})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil {
			pid, _ = strconv.Atoi(string(data))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		cancel()
		t.Fatal("ImageGen worker did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error after cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not settle canceled ImageGen operation")
	}
	page, err := store.Items(context.Background(), session.ID(sessionID), sessionstore.BeforeFirst, 100)
	if err != nil {
		t.Fatal(err)
	}
	var terminalCanceled bool
	for _, item := range page.Items {
		if item.Kind != sessionstore.ItemToolCallStatus {
			continue
		}
		status := item.Data.(sessionstore.ToolCallStatus)
		for _, current := range status.Operations {
			if current.Type == operation.TypeRemoteJob && current.Status == operation.StatusCanceled {
				terminalCanceled = true
			}
		}
	}
	if !terminalCanceled {
		t.Fatal("canceled ImageGen operation was not durably recorded")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("ImageGen worker %d survived cancellation: %v", pid, err)
	}
}

func TestProviderFailureReapsActiveBashBeforeRunReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	workspace := t.TempDir()
	marker := workspace + "/child.pid"
	longArgs, err := json.Marshal(map[string]string{"command": "printf '%s' $$ > '" + marker + "'; exec sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	triggerArgs, err := json.Marshal(map[string]string{"command": "sleep 1.25; printf trigger-next-turn"})
	if err != nil {
		t.Fatal(err)
	}
	providerErr := errors.New("provider unavailable")
	adapter := &providerFailureAfterGrace{
		first: llm.Response{ID: "start-child", Stop: llm.StopComplete, Output: []llm.Item{
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "long-call", Name: "Bash", Arguments: string(longArgs)}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "trigger-call", Name: "Bash", Arguments: string(triggerArgs)}},
		}},
		err: providerErr,
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, runner.Options{Prompt: "start a long task", SessionDir: t.TempDir(), Workspace: workspace, Adapter: adapter})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(marker)
		if readErr == nil {
			pid, _ = strconv.Atoi(string(data))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("Bash child did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, providerErr) {
			t.Fatalf("Run() error = %v, want provider error", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return after provider failure")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("Bash process %d was still present when Run returned (kill 0 error %v)", pid, err)
	}
}

func TestRunUsesBuiltinWriteFileAndEditFileTools(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{"path": "pkg/generated.go", "content": "package generated\nfunc Value() int { return 1 }\n"})
	if err != nil {
		t.Fatal(err)
	}
	editArgs, err := json.Marshal(map[string]any{"path": "pkg/generated.go", "old_string": "return 1", "new_string": "return 2"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{responses: []llm.Response{
		{ID: "write-file", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "write-call", Name: "WriteFile", Arguments: string(args)}}}},
		{ID: "edit-file", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "edit-call", Name: "EditFile", Arguments: string(editArgs)}}}},
		{ID: "file-tools-done", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "file updated"}}}},
	}}
	result, err := runner.Run(t.Context(), runner.Options{Prompt: "create and update the file", Workspace: workspace, SessionDir: t.TempDir(), Adapter: adapter})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "file updated\n" {
		t.Fatalf("final result=%q", result.Text)
	}
	contents, err := os.ReadFile(filepath.Join(workspace, "pkg", "generated.go"))
	if err != nil || string(contents) != "package generated\nfunc Value() int { return 2 }\n" {
		t.Fatalf("file=%q err=%v", contents, err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.requests) != 3 {
		t.Fatalf("requests=%d want write/edit/final", len(adapter.requests))
	}
	foundToolResult := false
	for _, item := range adapter.requests[2].Input {
		if item.Type != llm.ItemToolResult {
			continue
		}
		toolResult, ok := item.Data.(llm.ToolResult)
		if !ok {
			continue
		}
		for _, output := range toolResult.Output {
			if strings.Contains(output.Value, "sha256") && !strings.Contains(output.Value, "package generated") {
				foundToolResult = true
			}
		}
	}
	if !foundToolResult {
		t.Fatal("model did not receive concise non-content file operation results")
	}
}
