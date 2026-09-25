package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsontext "encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/auth"
	"github.com/pkyanam/pk/internal/clipboard"
	"github.com/pkyanam/pk/internal/config"
	"github.com/pkyanam/pk/internal/goals"
	"github.com/pkyanam/pk/internal/imagegen"
	"github.com/pkyanam/pk/internal/modelstream"
	"github.com/pkyanam/pk/internal/runner"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func TestGoalRPCStartPauseResumeLifecycle(t *testing.T) {
	var output bytes.Buffer
	server := &rpcServer{ctx: context.Background(), output: &output, session: "goal-session", requestTypes: map[string]string{}, goalStore: goals.NewStore(t.TempDir())}
	server.handleGoal("set", "set", "Finish the integration and verify it")
	var event rpcEvent
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "goal_state" || event.Payload.(map[string]any)["start"] != true {
		t.Fatalf("set event = %+v", event)
	}
	output.Reset()
	server.handleGoal("pause", "pause", "")
	output.Reset()
	server.handleGoal("resume", "resume", "")
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "goal_state" || event.Payload.(map[string]any)["start"] != true {
		t.Fatalf("resume event = %+v", event)
	}
	g, ok, err := server.goalStore.Get(context.Background(), "goal-session")
	if err != nil || !ok || g.Status != goals.Active {
		t.Fatalf("goal after resume = %+v, %t, %v", g, ok, err)
	}
}

type rpcEventSink struct{ events chan []byte }

type clipboardFixture struct{ snapshot clipboard.Snapshot }

func (f clipboardFixture) Read(context.Context) (clipboard.Snapshot, error) { return f.snapshot, nil }

func TestRPCClipboardPasteReadsOnlyOnExplicitRequestAndReturnsSavedImage(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	workspace := t.TempDir()
	imageBytes := []byte("\x89PNG\r\n\x1a\nfixture")
	sink := &rpcEventSink{events: make(chan []byte, 2)}
	server := &rpcServer{
		ctx: context.Background(), output: sink, diagnostics: io.Discard,
		started: true, opts: runner.Options{Workspace: workspace},
		requestTypes: make(map[string]string),
		clipboardProvider: clipboardFixture{snapshot: clipboard.Snapshot{
			Text: "filename.png", Image: &clipboard.Image{ContentType: "image/png", Bytes: imageBytes},
		}},
	}
	server.handle(rpcMessage{Version: 1, ID: "paste-1", Type: "clipboard_paste"}, make(chan turnDone, 1))
	event := readRPCEvent(t, sink)
	payload, ok := event.Payload.(map[string]any)
	if event.Type != "clipboard_files" || event.ID != "paste-1" || !ok || payload["text"] != "" {
		t.Fatalf("unexpected event: %+v", event)
	}
	filesJSON, err := json.Marshal(payload["files"])
	if err != nil {
		t.Fatal(err)
	}
	var files []clipboard.SelectedFile
	if err := json.Unmarshal(filesJSON, &files); err != nil || len(files) != 1 {
		t.Fatalf("decode clipboard files: files=%+v err=%v", files, err)
	}
	saved, err := os.ReadFile(files[0].Path)
	if err != nil || !bytes.Equal(saved, imageBytes) {
		t.Fatalf("saved image=%q err=%v", saved, err)
	}
}

func TestRPCImageGenOptInPersistsAndAppearsInFreshToolCatalog(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_HOME", home)
	workspace := t.TempDir()
	var stdout bytes.Buffer
	server := &rpcServer{
		ctx: context.Background(), output: &stdout, diagnostics: io.Discard,
		cfgPath: filepath.Join(home, "config.json"), sessionDir: filepath.Join(home, "sessions"),
		started: true, opts: runner.Options{Workspace: workspace, SessionDir: filepath.Join(home, "sessions"), SkillsDirs: nil},
		requestTypes: make(map[string]string),
	}
	server.handle(rpcMessage{Version: 1, ID: "enable-image", Type: "image_configure", Payload: json.RawMessage(`{"enabled":true}`)}, make(chan turnDone, 1))
	var enabled struct {
		Type    string `json:"type"`
		Payload struct {
			Enabled         bool   `json:"enabled"`
			Driver          string `json:"driver"`
			NextSessionOnly bool   `json:"next_session_only"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &enabled); err != nil {
		t.Fatalf("decode enable response: %v; output=%q", err, stdout.String())
	}
	if enabled.Type != "image_configured" || !enabled.Payload.Enabled || enabled.Payload.Driver != config.DefaultImageGenDriver || !enabled.Payload.NextSessionOnly {
		t.Fatalf("enable response=%+v", enabled)
	}
	cfg, err := config.Load(server.cfgPath)
	if err != nil || cfg.ImageGenDriver != config.DefaultImageGenDriver {
		t.Fatalf("persisted config=%+v err=%v", cfg, err)
	}
	catalog, err := server.modelToolCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range catalog.Tools {
		if item.Name == imagegen.ToolName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("configured ImageGen missing from fresh RPC tool preview: %#v", catalog.Tools)
	}
	stdout.Reset()
	server.handle(rpcMessage{Version: 1, ID: "disable-image", Type: "image_configure", Payload: json.RawMessage(`{"enabled":false}`)}, make(chan turnDone, 1))
	cfg, err = config.Load(server.cfgPath)
	if err != nil || cfg.ImageGenDriver != "" {
		t.Fatalf("disabled config=%+v err=%v", cfg, err)
	}
}

func (sink *rpcEventSink) Write(data []byte) (int, error) {
	copy := append([]byte(nil), data...)
	select {
	case sink.events <- copy:
	default:
		return 0, errors.New("test event buffer full")
	}
	return len(data), nil
}

func TestRPCRejectsUnsupportedVersionAsVersionedJSONL(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := rpcMain(context.Background(), strings.NewReader(`{"version":9,"id":"c1","type":"start"}`+"\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rpcMain exit = %d; stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("diagnostics leaked for protocol error: %q", stderr.String())
	}
	var event rpcEvent
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &event); err != nil {
		t.Fatalf("decode response: %v; output=%q", err, stdout.String())
	}
	if event.Version != 1 || event.ID != "c1" || event.Type != "error" {
		t.Fatalf("event=%+v", event)
	}
}

func TestRPCReadySurvivesMissingCredentialsSoLoginCanRecover(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_HOME", home)
	workspace := t.TempDir()
	reader, writer := io.Pipe()
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- rpcMain(context.Background(), reader, sink, &stderr) }()
	if _, err := fmt.Fprintf(writer, "%s\n%s\n",
		`{"version":1,"id":"start-1","type":"start","payload":{"workspace":"`+workspace+`"}}`,
		`{"version":1,"id":"prompt-1","type":"prompt","payload":{"text":"hello"}}`); err != nil {
		t.Fatal(err)
	}
	var events []rpcEvent
	var payload map[string]any
	deadline := time.After(5 * time.Second)
	for payload == nil {
		select {
		case data := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			events = append(events, event)
			if event.Type == "error" && event.ID == "prompt-1" {
				payload = event.Payload.(map[string]any)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for missing-credential error: %+v", events)
		}
	}
	if len(events) == 0 || events[0].Type != "ready" {
		t.Fatalf("events=%+v", events)
	}
	if payload == nil || payload["request_type"] != "prompt" {
		t.Fatalf("missing recoverable prompt error: %+v", events)
	}
	if _, err := fmt.Fprintln(writer, `{"version":1,"id":"shutdown-1","type":"shutdown"}`); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("rpcMain exit=%d stderr=%q", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC server did not stop")
	}
}

func TestRPCLoadsDefaultSkillDirsAndResumeKeepsSavedSkillCatalog(t *testing.T) {
	home, pkHomeDir := t.TempDir(), t.TempDir()
	if err := os.Chmod(pkHomeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PK_HOME", pkHomeDir)
	writeSkill := func(parent, name, description string) string {
		t.Helper()
		directory := filepath.Join(parent, name)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "SKILL.md")
		body := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\nInstructions for %s.\n", name, description, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	codexRoot := filepath.Join(home, ".codex", "skills")
	agentsRoot := filepath.Join(home, ".agents", "skills")
	codexSkill := writeSkill(codexRoot, "codex-helper", "Codex helper instructions.")
	agentsSkill := writeSkill(agentsRoot, "agent-helper", "Agent helper instructions.")
	workspace := t.TempDir()
	sessions := filepath.Join(pkHomeDir, "sessions")
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "skill-first", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "first"}}}}},
		{response: llm.Response{ID: "skill-resume", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "resumed"}}}}},
	}}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	var diagnostics bytes.Buffer
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &diagnostics, cfgPath: filepath.Join(pkHomeDir, "config.json"), sessionDir: sessions, adapter: adapter, requestTypes: map[string]string{}}
	server.handle(rpcMessage{Version: 1, ID: "start", Type: "start", Payload: json.RawMessage(fmt.Sprintf(`{"workspace":%q}`, workspace))}, make(chan turnDone, 1))
	if !server.started {
		select {
		case event := <-sink.events:
			t.Fatalf("RPC start failed; event=%s", event)
		default:
			t.Fatal("RPC start failed without an event")
		}
	}
	wantDirs := []string{filepath.Join(home, ".codex", "skills"), filepath.Join(home, ".agents", "skills"), filepath.Join(pkHomeDir, "skills")}
	if len(server.opts.SkillsDirs) != 4 || fmt.Sprint(server.opts.SkillsDirs[:3]) != fmt.Sprint(wantDirs) {
		t.Fatalf("RPC skill dirs=%q, want %q", server.opts.SkillsDirs, wantDirs)
	}
	runPrompt := func(id, text string) turnDone {
		t.Helper()
		finished := make(chan turnDone, 1)
		payload, _ := json.Marshal(map[string]string{"text": text})
		server.handle(rpcMessage{Version: 1, ID: id, Type: "prompt", Payload: payload}, finished)
		select {
		case result := <-finished:
			if result.err != nil {
				t.Fatalf("prompt %s: %v", id, result.err)
			}
			return result
		case <-time.After(8 * time.Second):
			model.mu.Lock()
			calls := model.calls
			model.mu.Unlock()
			t.Fatalf("prompt %s did not finish; active=%v session=%q calls=%d diagnostics=%q", id, server.active, server.session, calls, diagnostics.String())
			return turnDone{}
		}
	}
	first := runPrompt("first", "use the default skills")
	if first.id == "" || strings.TrimSpace(first.text) != "first" || server.session == "" {
		t.Fatalf("first result=%+v session=%q", first, server.session)
	}
	server.completeTurn(first)
	digest := sha256.Sum256([]byte(server.session))
	snapshotPath := filepath.Join(sessions, hex.EncodeToString(digest[:])+".context.json")
	loadSnapshot := func() runner.ContextSnapshot {
		t.Helper()
		data, err := os.ReadFile(snapshotPath)
		if err != nil {
			t.Fatalf("read session context: %v", err)
		}
		var snapshot runner.ContextSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			t.Fatalf("decode session context: %v", err)
		}
		return snapshot
	}
	assertOriginalSkills := func(snapshot runner.ContextSnapshot) {
		t.Helper()
		if len(snapshot.Skills) != 3 {
			t.Fatalf("saved skills=%+v", snapshot.Skills)
		}
		got := map[string]string{}
		for _, skill := range snapshot.Skills {
			got[skill.Name] = skill.Path
		}
		if got["codex-helper"] != codexSkill || got["agent-helper"] != agentsSkill {
			t.Fatalf("saved skill catalog=%v", got)
		}
		if got["pk"] == "" {
			t.Fatal("bundled pk skill missing from saved catalog")
		}
	}
	assertOriginalSkills(loadSnapshot())
	writeSkill(agentsRoot, "later-skill", "Added after this session began.")
	second := runPrompt("resume", "continue with the saved context")
	if strings.TrimSpace(second.text) != "resumed" {
		t.Fatalf("resume result=%+v", second)
	}
	server.completeTurn(second)
	assertOriginalSkills(loadSnapshot())
	_ = adapter.Close()
}

func TestLocateUIEntryHonorsConfiguredAsset(t *testing.T) {
	entry := filepath.Join(t.TempDir(), "main.js")
	if err := os.WriteFile(entry, []byte("// tui"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_UI_ENTRY", entry)
	got, err := locateUIEntry(filepath.Join(t.TempDir(), "pk"))
	if err != nil {
		t.Fatal(err)
	}
	if got != entry {
		t.Fatalf("entry=%q want %q", got, entry)
	}
}

func TestLocateUIEntryUsesInstalledExecutableFromAnotherWorkingDirectory(t *testing.T) {
	t.Setenv("PK_UI_ENTRY", "")
	root := t.TempDir()
	binDir := filepath.Join(root, ".local", "bin")
	uiDir := filepath.Join(root, ".local", "lib", "pk", "ui", "dist")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(uiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(binDir, "pk")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(uiDir, "main.js")
	if err := os.WriteFile(entry, []byte("// tui"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	got, err := locateUIEntry(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got != entry {
		t.Fatalf("entry=%q want %q", got, entry)
	}
}

func TestRecentSessionHistoryReplaysOnlyUserAndAssistantMessages(t *testing.T) {
	ctx := context.Background()
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("session-history")
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	input := inbox.Input{ID: "input-1", Kind: inbox.InputExternal, Payload: jsontext.Value(`"user prompt"`)}
	if err := store.AppendInput(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	turnID := session.TurnID("turn-1")
	if err := store.AppendTurn(ctx, id, session.Turn{ID: turnID, Type: session.TurnRegular}); err != nil {
		t.Fatal(err)
	}
	response := sessionstore.ModelResponse{TurnID: turnID, Response: llm.Response{ID: "response-1", Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "analysis", Phase: "analysis"}}, {Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "answer", Phase: "final"}}}}}
	if err := store.AppendModelResponse(ctx, id, response); err != nil {
		t.Fatal(err)
	}
	entries, truncated, err := recentSessionHistory(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(entries) != 2 {
		t.Fatalf("history=%+v truncated=%v", entries, truncated)
	}
	if entries[0].Role != "user" || entries[0].Text != "user prompt" || entries[1].Role != "assistant" || entries[1].Text != "answer" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestRecentSessionHistoryKeepsLatestRowsBeyondPageWindow(t *testing.T) {
	ctx := context.Background()
	store, err := localfile.New(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	id := session.ID("session-long")
	if _, err := store.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	const total = 820
	for i := 0; i < total; i++ {
		text := "prompt-" + strconv.Itoa(i)
		payload, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		input := inbox.Input{ID: inbox.ID("input-" + strconv.Itoa(i)), Kind: inbox.InputExternal, Payload: jsontext.Value(payload)}
		if err := store.AppendInput(ctx, id, input); err != nil {
			t.Fatal(err)
		}
	}
	entries, truncated, err := recentSessionHistory(ctx, store, string(id))
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(entries) != maxHistoryEntries {
		t.Fatalf("rows=%d truncated=%v", len(entries), truncated)
	}
	if entries[0].Text != "prompt-720" || entries[len(entries)-1].Text != "prompt-819" {
		t.Fatalf("history window starts/ends at %q/%q", entries[0].Text, entries[len(entries)-1].Text)
	}
	if strings.Contains(entries[0].Text, "prompt-0") {
		t.Fatalf("old prefix appeared in tail window: %q", entries[0].Text)
	}
}

func TestTailUTF8TruncationKeepsValidText(t *testing.T) {
	got := tailUTF8("before 🌙 after", 8)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "after") {
		t.Fatalf("tail=%q valid=%v", got, utf8.ValidString(got))
	}
}

func TestRPCPromptAttachmentsReachModelAsNewInput(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	selectedDir := t.TempDir() // Explicit user-selected input may live outside workspace.
	file := filepath.Join(selectedDir, "reference.txt")
	const marker = "ATTACHMENT_SENTINEL_9721"
	if err := os.WriteFile(file, []byte("reference body: "+marker), 0o600); err != nil {
		t.Fatal(err)
	}
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "done", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Read the file."}}}}}}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	payload, _ := json.Marshal(map[string]any{"text": "Summarize this reference.", "files": []string{file}})
	server.handle(rpcMessage{Version: 1, ID: "attached-turn", Type: "prompt", Payload: payload}, finished)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("runner error: %v", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not finish")
	}
	model.mu.Lock()
	requests := append([]llm.Request(nil), model.requests...)
	model.mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("model received no requests")
	}
	var found bool
	for _, request := range requests {
		for _, item := range request.Input {
			if message, ok := item.Data.(llm.Message); ok && strings.Contains(message.Text, marker) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("selected file content did not reach model input; requests=%+v", requests)
	}
	_ = server.adapter.Close()
}

func TestRPCPromptUsesAdapterPreparedDuringAsyncSetup(t *testing.T) {
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "reply", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "adapter is ready"}}}}}}}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	workspace, sessions := t.TempDir(), t.TempDir()
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, requestTypes: map[string]string{}, prepareAdapter: func(context.Context, bool, string) (*codexAdapter, error) { return adapter, nil }}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "prepared-turn", Type: "prompt", Payload: json.RawMessage(`{"text":"hello"}`)}, finished)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("runner error: %v", result.err)
		}
		if strings.TrimSpace(result.text) != "adapter is ready" {
			t.Fatalf("result text=%q", result.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not finish")
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls == 0 {
		t.Fatal("prepared adapter was not used by runner")
	}
	if server.adapter != adapter {
		t.Fatal("prepared adapter was not retained on RPC server")
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = adapter.Close()
}

func TestRPCCompletesTwoSequentialPromptsOnOneSession(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "first", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "first answer"}}}}},
		{response: llm.Response{ID: "second", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "second answer"}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	reader, writer := io.Pipe()
	server := &rpcServer{ctx: context.Background(), input: reader, output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	served := make(chan error, 1)
	go func() { served <- server.serve() }()
	writeCommand := func(id, text string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"text": text})
		command, _ := json.Marshal(rpcMessage{Version: 1, ID: id, Type: "prompt", Payload: payload})
		if _, err := writer.Write(append(command, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	waitTurn := func(id string) {
		t.Helper()
		timer := time.NewTimer(8 * time.Second)
		defer timer.Stop()
		for {
			select {
			case raw := <-sink.events:
				var event rpcEvent
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				if event.ID == id && event.Type == "turn_finished" {
					return
				}
				if event.ID == id && event.Type == "error" {
					t.Fatalf("prompt %s failed: %v", id, event.Payload)
				}
			case <-timer.C:
				t.Fatalf("prompt %s did not finish", id)
			}
		}
	}
	writeCommand("prompt-one", "first prompt")
	waitTurn("prompt-one")
	server.mu.Lock()
	if server.active {
		t.Fatal("server remained active after first turn_finished")
	}
	sessionID := server.session
	server.mu.Unlock()
	if sessionID == "" {
		t.Fatal("first prompt did not create a session")
	}
	writeCommand("prompt-two", "second prompt")
	waitTurn("prompt-two")
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls=%d, want two", calls)
	}
	shutdown, _ := json.Marshal(rpcMessage{Version: 1, ID: "shutdown", Type: "shutdown"})
	_, _ = writer.Write(append(shutdown, '\n'))
	_ = writer.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC server did not stop after two turns")
	}
}

func TestRPCTurnFinishesAfterToolThenAssistantResponse(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": "printf rpc-tool-result"})
	workspace, sessions := t.TempDir(), t.TempDir()
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "tool-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "shell-1", Name: "Bash", Arguments: string(args)}}}}},
		{response: llm.Response{ID: "final-turn", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "command complete"}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 64)}
	reader, writer := io.Pipe()
	server := &rpcServer{ctx: context.Background(), input: reader, output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	served := make(chan error, 1)
	go func() { served <- server.serve() }()
	prompt, _ := json.Marshal(rpcMessage{Version: 1, ID: "tool-turn", Type: "prompt", Payload: json.RawMessage(`{"text":"run a command"}`)})
	if _, err := writer.Write(append(prompt, '\n')); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	var assistantSeen bool
	for {
		select {
		case raw := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "assistant" && strings.Contains(fmt.Sprint(event.Payload), "command complete") {
				assistantSeen = true
			}
			if event.ID == "tool-turn" && event.Type == "turn_finished" {
				if !assistantSeen {
					t.Fatal("turn_finished arrived before the final assistant event")
				}
				server.mu.Lock()
				active := server.active
				server.mu.Unlock()
				if active {
					t.Fatal("server remained active after tool turn completion")
				}
				goto finished
			}
		case <-deadline.C:
			t.Fatal("RPC turn did not finish after Bash and assistant response")
		}
	}

finished:
	shutdown, _ := json.Marshal(rpcMessage{Version: 1, ID: "shutdown", Type: "shutdown"})
	_, _ = writer.Write(append(shutdown, '\n'))
	_ = writer.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC server did not stop after tool turn")
	}
}

func TestRPCCancelInterruptsBlockedAttachmentSetup(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	entered := make(chan struct{})
	model := &mockModelAdapter{replies: []adapterReply{{response: llm.Response{ID: "unexpected", Stop: llm.StopComplete}}}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	server.loadAttachments = func(ctx context.Context, workspace, prompt string, paths []string) (string, []attachments.Attachment, error) {
		close(entered)
		<-ctx.Done()
		return "", nil, ctx.Err()
	}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "blocked-turn", Type: "prompt", Payload: json.RawMessage(`{"text":"read it","files":["slow.pdf"]}`)}, finished)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("attachment setup did not start asynchronously")
	}
	server.handle(rpcMessage{Version: 1, ID: "cancel-setup", Type: "cancel"}, finished)
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("setup result error=%v, want context.Canceled", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not interrupt blocked attachment setup")
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 0 {
		t.Fatalf("model calls=%d, expected cancellation before model run", calls)
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = server.adapter.Close()
}

func TestRPCQuestionUsesBrokerAndContinuesRunner(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	args, err := json.Marshal(map[string]any{"question": "Choose a direction?", "choices": []string{"A", "B"}})
	if err != nil {
		t.Fatal(err)
	}
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "ask", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "question-1", Name: "AskUser", Arguments: string(args)}}}}},
		{response: llm.Response{ID: "final", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Thanks, proceeding with A."}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "turn-1", Type: "prompt", Payload: json.RawMessage(`{"text":"Ask me before choosing."}`)}, finished)
	var seenQuestion, seenAnswer, seenAssistant bool
	var eventTypes []string
	deadline := time.After(10 * time.Second)
	for !seenAssistant {
		select {
		case data := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			eventTypes = append(eventTypes, event.Type)
			switch event.Type {
			case "question":
				question := event.Payload.(map[string]any)
				if question["id"] != "question-1" || question["text"] != "Choose a direction?" {
					t.Fatalf("question=%v", question)
				}
				seenQuestion = true
				server.handle(rpcMessage{Version: 1, ID: "answer-1", Type: "answer_question", Payload: json.RawMessage(`{"id":"question-1","answer":"A"}`)}, finished)
			case "question_answered":
				seenAnswer = true
			case "assistant":
				seenAssistant = true
			}
		case <-deadline:
			select {
			case result := <-finished:
				t.Fatalf("timed out; runner returned text=%q error=%v; events=%v", result.text, result.err, eventTypes)
			default:
			}
			t.Fatalf("timed out waiting for question and final assistant answer; events=%v", eventTypes)
		}
	}
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("runner failed after answer: %v", result.err)
		}
		if !strings.Contains(result.text, "Thanks, proceeding with A.") {
			t.Fatalf("final text=%q", result.text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not finish after answer")
	}
	if !seenQuestion || !seenAnswer || !seenAssistant {
		t.Fatalf("events: question=%v answered=%v assistant=%v", seenQuestion, seenAnswer, seenAssistant)
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = server.adapter.Close()
}

func TestRPCModelProgressPrecedesAuthoritativeAssistantAndIsNotSavedAsHistory(t *testing.T) {
	streamed := make(chan struct{})
	release := make(chan struct{})
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"message-1\",\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"message-1\",\"delta\":\"DRAFT_ONLY_TOKEN\"}\n\n")
		flusher.Flush()
		close(streamed)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-release:
				goto complete
			case <-ticker.C:
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"message-1\",\"delta\":\" more\"}\n\n")
				flusher.Flush()
			}
		}
	complete:
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response-1\",\"status\":\"completed\",\"output\":[{\"id\":\"message-1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"AUTHORITATIVE_FINAL\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		flusher.Flush()
	}))
	defer serverHTTP.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	workspace, sessions := t.TempDir(), t.TempDir()
	model, err := modelstream.NewClient(modelstream.Config{AccessToken: "fake-token", AccountID: "fake-account", BaseURL: serverHTTP.URL})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &codexAdapter{credential: auth.Credential{AccessToken: "fake-token", AccountID: "fake-account"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}
	sink := &rpcEventSink{events: make(chan []byte, 128)}
	rpc := &rpcServer{ctx: context.Background(), output: sink, diagnostics: io.Discard, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: adapter, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	rpc.handle(rpcMessage{Version: 1, ID: "streamed-prompt", Type: "prompt", Payload: json.RawMessage(`{"text":"answer the question"}`)}, finished)
	select {
	case <-streamed:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not send its first text delta")
	}
	var progress rpcEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case raw := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "model_progress" {
				if payload, ok := event.Payload.(map[string]any); ok && payload["phase"] == "assistant_delta" {
					progress = event
					break
				}
			}
		case <-deadline:
			t.Fatal("RPC did not emit model_progress before response completion")
		}
		if progress.Type != "" {
			break
		}
	}
	if progress.ID != "streamed-prompt" {
		t.Fatalf("progress envelope ID=%q", progress.ID)
	}
	progressPayload, ok := progress.Payload.(map[string]any)
	if !ok || progressPayload["phase"] != "assistant_delta" || !strings.Contains(fmt.Sprint(progressPayload["text_delta"]), "DRAFT_ONLY_TOKEN") {
		t.Fatalf("progress payload=%v", progress.Payload)
	}
	rpc.mu.Lock()
	sessionID := rpc.session
	rpc.mu.Unlock()
	if sessionID == "" {
		t.Fatal("session was not created before streaming started")
	}
	store, err := localfile.New(sessions)
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := recentSessionHistory(context.Background(), store, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(before), "DRAFT_ONLY_TOKEN") {
		t.Fatalf("partial model text was persisted as authoritative history: %+v", before)
	}
	close(release)
	select {
	case result := <-finished:
		if result.err != nil {
			t.Fatalf("RPC runner returned error: %v", result.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("RPC did not finish after final response was released")
	}
	rpc.completeTurn(turnDone{id: "streamed-prompt"})
	after, _, err := recentSessionHistory(context.Background(), store, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	joined := fmt.Sprint(after)
	if !strings.Contains(joined, "AUTHORITATIVE_FINAL") || strings.Contains(joined, "DRAFT_ONLY_TOKEN") {
		t.Fatalf("history does not contain only authoritative output: %+v", after)
	}
	var finalSeen bool
	for {
		select {
		case raw := <-sink.events:
			var event rpcEvent
			if json.Unmarshal(raw, &event) == nil && event.ID == "streamed-prompt" && event.Type == "assistant" {
				finalSeen = true
			}
		default:
			if !finalSeen {
				t.Fatal("final authoritative assistant event was not emitted")
			}
			_ = adapter.Close()
			return
		}
	}
}

func TestRPCCancelQuestionStopsTurnWithoutContinuing(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	args, _ := json.Marshal(map[string]any{"question": "Confirm destructive change?", "kind": "confirmation"})
	model := &mockModelAdapter{replies: []adapterReply{
		{response: llm.Response{ID: "ask", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "question-cancel", Name: "AskUser", Arguments: string(args)}}}}},
		{response: llm.Response{ID: "should-not-run", Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "continued without consent"}}}}},
	}}
	sink := &rpcEventSink{events: make(chan []byte, 32)}
	server := &rpcServer{ctx: context.Background(), output: sink, diagnostics: &bytes.Buffer{}, cfgPath: filepath.Join(t.TempDir(), "config.json"), sessionDir: sessions, started: true, opts: runner.Options{Workspace: workspace, SessionDir: sessions, Model: "gpt-6-luna", Effort: "medium"}, adapter: &codexAdapter{credential: auth.Credential{AccessToken: "fake"}, client: model, useCodex: true, semaphore: make(chan struct{}, 1)}, requestTypes: map[string]string{}}
	finished := make(chan turnDone, 1)
	server.handle(rpcMessage{Version: 1, ID: "turn-cancel", Type: "prompt", Payload: json.RawMessage(`{"text":"Do the potentially destructive work."}`)}, finished)
	deadline := time.After(10 * time.Second)
	var questionSeen, canceledEvent bool
	for !canceledEvent {
		select {
		case data := <-sink.events:
			var event rpcEvent
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "question" {
				questionSeen = true
				server.handle(rpcMessage{Version: 1, ID: "cancel-1", Type: "cancel_question", Payload: json.RawMessage(`{"id":"question-cancel"}`)}, finished)
			}
			if event.Type == "question_cancelled" {
				canceledEvent = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for cancellation event")
		}
	}
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("runner error=%v, want cancellation", result.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner stayed blocked after question cancellation")
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if !questionSeen || calls != 1 {
		t.Fatalf("question=%v model calls=%d, want one call and no continuation", questionSeen, calls)
	}
	if server.broker != nil {
		server.broker.Close()
	}
	_ = server.adapter.Close()
}

func TestConfigCommandPersistsSelectedDefaults(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := runConfigCommand([]string{"set", "model", "gpt-6-astra"}, &out, &errOut); code != 0 {
		t.Fatalf("set model exit=%d: %s", code, errOut.String())
	}
	if code := runConfigCommand([]string{"set", "effort", "high"}, &out, &errOut); code != 0 {
		t.Fatalf("set effort exit=%d: %s", code, errOut.String())
	}
	if code := runConfigCommand([]string{"set", "context-policy", "compact"}, &out, &errOut); code != 0 {
		t.Fatalf("set context policy exit=%d: %s", code, errOut.String())
	}
	if code := runConfigCommand([]string{"set", "image-driver", "gpt-6-astra"}, &out, &errOut); code != 0 {
		t.Fatalf("set image driver exit=%d: %s", code, errOut.String())
	}
	if code := runConfigCommand([]string{"set", "theme", "HIGH-CONTRAST"}, &out, &errOut); code != 0 {
		t.Fatalf("set theme exit=%d: %s", code, errOut.String())
	}
	options, _, _, err := parseRunArgs([]string{"-p", "hello", "--workspace", t.TempDir()}, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if options.Model != "gpt-6-astra" || options.Effort != "high" || !options.CompactCapturedOutput {
		t.Fatalf("parsed model/effort/compact=%s/%s/%v", options.Model, options.Effort, options.CompactCapturedOutput)
	}
	options, _, _, err = parseRunArgs([]string{"-p", "hello", "--workspace", t.TempDir(), "--context-policy", "full"}, &errOut)
	if err != nil || options.CompactCapturedOutput {
		t.Fatalf("explicit full policy options=%+v err=%v", options, err)
	}
	if code := runConfigCommand([]string{"show"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "context_policy = compact") || !strings.Contains(out.String(), "theme = high-contrast") || !strings.Contains(out.String(), "image_driver = gpt-6-astra") {
		t.Fatalf("show context policy exit=%d output=%q error=%q", code, out.String(), errOut.String())
	}
	if code := runConfigCommand([]string{"set", "theme", "neon"}, &out, &errOut); code != 2 {
		t.Fatalf("invalid theme exit=%d: %s", code, errOut.String())
	}
	cfg, err := config.Load(filepath.Join(pkHome(), "config.json"))
	if err != nil || cfg.Theme != config.ThemeHighContrast || cfg.Model != "gpt-6-astra" || cfg.Effort != "high" || cfg.ContextPolicy != config.ContextPolicyCompact {
		t.Fatalf("config after theme selection=%+v err=%v", cfg, err)
	}
	if code := runConfigCommand([]string{"set", "image-driver", "off"}, &out, &errOut); code != 0 {
		t.Fatalf("disable image driver exit=%d: %s", code, errOut.String())
	}
	cfg, err = config.Load(filepath.Join(pkHome(), "config.json"))
	if err != nil || cfg.ImageGenDriver != "" {
		t.Fatalf("disabled image driver config=%+v err=%v", cfg, err)
	}
}

func TestConfigRejectsUnsupportedNoneEffort(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := runConfigCommand([]string{"set", "effort", "none"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "not supported by the current adapter") {
		t.Fatalf("code=%d error=%q", code, errOut.String())
	}
}

func TestTaskCreateRejectsUnknownContextPolicyBeforeStartingWorker(t *testing.T) {
	t.Setenv("PK_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	code := runTaskCommand(context.Background(), []string{"create", "-p", "do work", "--context-policy", "summarize-history"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "use full or compact") {
		t.Fatalf("code=%d error=%q", code, errOut.String())
	}
}
