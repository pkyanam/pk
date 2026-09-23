package runner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkyanam/pk/internal/extensions"
	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

type promptRegistryWorker struct{}

func (promptRegistryWorker) Call(_ context.Context, method string, input, output any) error {
	if method != "initialize" {
		return nil
	}
	paramsData, err := json.Marshal(input)
	if err != nil {
		return err
	}
	var params extensions.InitializeParams
	if err := json.Unmarshal(paramsData, &params); err != nil {
		return err
	}
	value := extensions.InitializeResult{APIVersion: extensions.ProtocolVersion, ID: params.ID, Tools: []string{"mail_inboxes"}}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}
func (promptRegistryWorker) Close() error { return nil }

func TestAssembledPromptDistinguishesLoadedSessionToolsFromWorkspaceDocs(t *testing.T) {
	workspace := t.TempDir()
	manifest := extensions.Manifest{
		APIVersion: extensions.ProtocolVersion,
		ID:         "agentmail-readonly",
		Version:    "0.1.0",
		Executable: "fixture-worker",
		Tools: []extensions.ToolSpec{{
			Name:        "mail_inboxes",
			Description: "List AgentMail inboxes.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		}},
	}
	host, report, err := extensions.NewHost(context.Background(), workspace, []extensions.Manifest{manifest}, func(context.Context, extensions.Manifest, string) (extensions.Worker, error) {
		return promptRegistryWorker{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if len(report.Loaded) != 1 || report.Loaded[0] != manifest.ID {
		t.Fatalf("plugin registration: %+v", report)
	}
	base := newToolRegistryBase(workspace, filepath.Join(workspace, ".pk", "operations"))
	registry, issues := extensions.DecorateRegistry(base, host)
	if len(issues) != 0 {
		t.Fatalf("plugin decoration: %v", issues)
	}
	snapshot := newContextSnapshot(Options{Workspace: workspace}, registry, nil)
	if len(snapshot.Tools) < 4 {
		t.Fatalf("expected core tools plus plugin, got %d", len(snapshot.Tools))
	}

	builder := contextbuilder.NewBuilder()
	builder.SetModel(llm.Model{ID: "gpt-6-luna"})
	builder.SetSystemPrompt(snapshot.SystemPrompt)
	for _, definition := range snapshot.Tools {
		builder.AddTool(definition)
	}
	assembled, err := (identityBuilder{Builder: builder, template: defaultIdentityTemplate}).Build()
	if err != nil {
		t.Fatal(err)
	}
	var systemPrompt string
	for _, item := range assembled.Request.Input {
		if item.Type != llm.ItemMessage {
			continue
		}
		message, ok := item.Data.(llm.Message)
		if ok && message.Role == llm.RoleSystem {
			systemPrompt = message.Text
			break
		}
	}
	if !containsText(systemPrompt, "report only the tools available in this session", "workspace documentation may describe tools that are not loaded") {
		t.Fatalf("assembled prompt lacks tool-availability boundary: %s", systemPrompt)
	}
	names := make(map[string]bool, len(assembled.Request.Tools))
	for _, definition := range assembled.Request.Tools {
		names[definition.Name] = true
	}
	for _, name := range []string{tool.BashName, tool.ViewImageName, tool.SkillUseName, "mail_inboxes"} {
		if !names[name] {
			t.Errorf("assembled request is missing active tool %q; tools=%v", name, names)
		}
	}
}

func containsText(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
