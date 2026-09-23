package runner

import (
	"context"
	"errors"
	"strings"
)

// PreviewToolCatalog builds the side-effect-free default registry preview used
// before a session has a frozen context snapshot. It does not start extension
// or MCP processes; callers may supply safe in-process decorators.
func PreviewToolCatalog(ctx context.Context, options Options) ([]SavedToolSummary, []string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(options.Workspace) == "" {
		return nil, nil, errors.New("workspace is required to preview model tools")
	}
	factory := options.RegistryFactory
	if factory == nil {
		factory = defaultRegistryFactory
	}
	registryOptions := ToolRegistryOptions{Workspace: options.Workspace, SkillsDirs: options.SkillsDirs}
	registry, _, warnings := factory(registryOptions)
	if registry == nil {
		return nil, nil, errors.New("tool registry factory returned nil")
	}
	if options.DecorateRegistry != nil {
		registry = options.DecorateRegistry(registry)
		if registry == nil {
			return nil, nil, errors.New("registry decorator returned nil")
		}
	}
	tools := make([]SavedToolSummary, 0, len(registry.StaticDefinitions()))
	for _, definition := range registry.StaticDefinitions() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := definition.Tool.Name
		if name == "" {
			continue
		}
		tools = append(tools, SavedToolSummary{Name: name, Description: definition.Tool.Description, Source: toolCatalogSource(name)})
	}
	warningText := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		if warning != nil {
			warningText = append(warningText, warning.Error())
		}
	}
	return tools, warningText, nil
}

func toolCatalogSource(name string) string {
	switch name {
	case "Bash", "ViewImage", "SkillUse":
		return "built-in"
	case "AskUser":
		return "interaction"
	case "ImageGen":
		return "image generation"
	case "SubagentStart", "SubagentStatus", "SubagentSend", "SubagentWait", "SubagentCancel":
		return "subagents"
	case "WebSearch", "WebFetch":
		return "web search"
	default:
		if strings.HasPrefix(name, "mcp_") {
			return "MCP"
		}
		return "extension"
	}
}
