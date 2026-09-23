package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/pkyanam/pk/internal/mcpclient"
	"github.com/pkyanam/pk/internal/runner"
)

type mcpCatalogPayload struct {
	Servers           []mcpclient.ServerSummary `json:"servers"`
	Tools             []mcpCatalogTool          `json:"tools"`
	SavedSessionTools bool                      `json:"saved_session_tools"`
	NextSessionOnly   bool                      `json:"next_session_only"`
}

type mcpCatalogTool struct {
	ServerID       string         `json:"server_id"`
	ServerToolName string         `json:"server_tool_name"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	InputSchema    map[string]any `json:"input_schema"`
}

type savedToolCatalogPayload struct {
	Tools       []runner.SavedToolSummary `json:"tools"`
	Saved       bool                      `json:"saved"`
	Initialized bool                      `json:"initialized"`
	Preview     bool                      `json:"preview"`
	Notice      string                    `json:"notice,omitempty"`
	Deferred    []string                  `json:"deferred,omitempty"`
	Warnings    []string                  `json:"warnings,omitempty"`
}

func (s *rpcServer) modelToolCatalog(ctx context.Context) (savedToolCatalogPayload, error) {
	s.mu.Lock()
	sessionDir, sessionID, workspace := s.sessionDir, s.session, s.opts.Workspace
	s.mu.Unlock()
	var err error
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return savedToolCatalogPayload{}, err
		}
	}
	tools, saved, err := runner.LoadSavedToolCatalog(ctx, sessionDir, sessionID, workspace)
	if err != nil {
		return savedToolCatalogPayload{}, err
	}
	if saved {
		return savedToolCatalogPayload{Tools: tools, Saved: true, Initialized: true}, nil
	}
	s.mu.Lock()
	pluginPaths := append([]string(nil), s.pluginPaths...)
	skillsDirs := append([]string(nil), s.opts.SkillsDirs...)
	imageDriver := s.opts.ImageGenFingerprint
	s.mu.Unlock()
	preview, err := s.previewModelToolCatalog(ctx, workspace, sessionDir, skillsDirs, len(pluginPaths) > 0, imageDriver)
	if err != nil {
		return savedToolCatalogPayload{}, err
	}
	return preview, nil
}

func (s *rpcServer) mcpCatalog(ctx context.Context) (mcpCatalogPayload, error) {
	store := mcpclient.ConfigStore{Home: pkHome()}
	servers, err := store.Summaries()
	if err != nil {
		return mcpCatalogPayload{}, err
	}
	s.mu.Lock()
	sessionDir, sessionID, workspace := s.sessionDir, s.session, s.opts.Workspace
	s.mu.Unlock()
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return mcpCatalogPayload{}, err
		}
	}
	tools, saved, err := runner.LoadSavedMCPTools(ctx, sessionDir, sessionID, workspace)
	if err != nil {
		return mcpCatalogPayload{}, err
	}
	payload := mcpCatalogPayload{Servers: servers, Tools: make([]mcpCatalogTool, 0, len(tools)), SavedSessionTools: saved, NextSessionOnly: true}
	serverIDs := make([]string, 0, len(servers))
	for _, server := range servers {
		serverIDs = append(serverIDs, server.ID)
	}
	for _, tool := range tools {
		serverID, serverToolName := splitMCPToolName(tool.Name, serverIDs)
		payload.Tools = append(payload.Tools, mcpCatalogTool{ServerID: serverID, ServerToolName: serverToolName, Name: tool.Name, Description: tool.Description, InputSchema: tool.Parameters})
	}
	return payload, nil
}

func (s *rpcServer) mcpAdd(ctx context.Context, config mcpclient.ServerConfig) (mcpCatalogPayload, error) {
	if err := ctx.Err(); err != nil {
		return mcpCatalogPayload{}, err
	}
	store := mcpclient.ConfigStore{Home: pkHome()}
	var addErr error
	if config.Auth.Mode == "oauth" {
		addErr = store.AddOAuth(config)
	} else {
		addErr = store.Add(config)
	}
	if addErr != nil {
		return mcpCatalogPayload{}, addErr
	}
	return s.mcpCatalog(ctx)
}

// mcpAddSecret accepts a one-shot credential from a masked UI field. It never
// includes the credential in returned RPC data or error text.
func (s *rpcServer) mcpAddSecret(ctx context.Context, config mcpclient.ServerConfig, credentialKind, secret string) (mcpCatalogPayload, error) {
	if err := ctx.Err(); err != nil {
		return mcpCatalogPayload{}, err
	}
	if err := (mcpclient.ConfigStore{Home: pkHome()}).AddWithSecret(config, credentialKind, secret); err != nil {
		return mcpCatalogPayload{}, errors.New("could not store MCP server credential; check endpoint and credential fields")
	}
	return s.mcpCatalog(ctx)
}

func (s *rpcServer) mcpRemove(ctx context.Context, id string) (mcpCatalogPayload, error) {
	if err := ctx.Err(); err != nil {
		return mcpCatalogPayload{}, err
	}
	if err := (mcpclient.ConfigStore{Home: pkHome()}).Remove(id); err != nil {
		return mcpCatalogPayload{}, err
	}
	return s.mcpCatalog(ctx)
}

func splitMCPToolName(name string, configuredIDs []string) (serverID, exportedToolName string) {
	// The exported name ends in an eight-character hash. The model-visible form
	// is opaque by design; expose the full export name as originalName when it
	// cannot be safely reconstructed from potentially punctuation-heavy inputs.
	const prefix = "mcp_"
	if len(name) < len(prefix) || name[:len(prefix)] != prefix {
		return "", name
	}
	for _, id := range configuredIDs {
		serverPrefix := prefix + id + "_"
		if strings.HasPrefix(name, serverPrefix) && len(id) > len(serverID) {
			serverID = id
			exportedToolName = strings.TrimPrefix(name, serverPrefix)
		}
	}
	if serverID == "" {
		return "", name
	}
	return serverID, exportedToolName
}
