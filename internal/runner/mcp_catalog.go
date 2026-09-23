package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
)

func validateMCPFingerprint(snapshot ContextSnapshot, current string) error {
	if snapshot.MCPFingerprint == current {
		return nil
	}
	return errors.New("MCP server configuration or tool schemas changed; restore the original MCP configuration or start a new session")
}

type SavedToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`
}

// LoadSavedToolCatalog reports the model-visible tool set frozen into a saved
// session. It does not include UI slash commands or discover live providers.
func LoadSavedToolCatalog(ctx context.Context, sessionDir, sessionID, workspace string) ([]SavedToolSummary, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return []SavedToolSummary{}, false, nil
	}
	snapshot, err := defaultContextSnapshotStore(sessionDir, workspace).LoadContext(ctx, session.ID(sessionID))
	if err != nil {
		if isMissingContextSnapshot(err) {
			return []SavedToolSummary{}, false, nil
		}
		return nil, false, err
	}
	summaries := make([]SavedToolSummary, 0, len(snapshot.Tools))
	for _, definition := range snapshot.Tools {
		source := ""
		switch definition.Name {
		case "Bash", "ViewImage", "SkillUse":
			source = "built-in"
		case "AskUser":
			source = "interaction"
		case "ImageGen":
			source = "image generation"
		default:
			if strings.HasPrefix(definition.Name, "mcp_") {
				source = "MCP"
			}
		}
		summaries = append(summaries, SavedToolSummary{Name: definition.Name, Description: definition.Description, Source: source})
	}
	return summaries, true, nil
}

// LoadSavedMCPTools returns MCP tool definitions captured in a session's
// context snapshot. It never connects to configured servers or rediscovers
// tools. saved is false when the session has no context snapshot.
func LoadSavedMCPTools(ctx context.Context, sessionDir, sessionID, workspace string) ([]llm.Tool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return []llm.Tool{}, false, nil
	}
	snapshot, err := defaultContextSnapshotStore(sessionDir, workspace).LoadContext(ctx, session.ID(sessionID))
	if err != nil {
		if isMissingContextSnapshot(err) {
			return []llm.Tool{}, false, nil
		}
		return nil, false, err
	}
	tools := make([]llm.Tool, 0)
	for _, definition := range snapshot.Tools {
		if !strings.HasPrefix(definition.Name, "mcp_") {
			continue
		}
		encoded, err := json.Marshal(definition)
		if err != nil {
			return nil, false, err
		}
		var copied llm.Tool
		if err := json.Unmarshal(encoded, &copied); err != nil {
			return nil, false, err
		}
		tools = append(tools, copied)
	}
	return tools, true, nil
}
