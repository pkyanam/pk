package main

import (
	"context"
	"errors"

	"github.com/pkyanam/pk/internal/mcpclient"
)

// startMCPOAuthLogin performs explicit OAuth consent outside the model turn.
// It deliberately emits only status metadata; authorization URLs, state,
// codes, and tokens never cross the RPC boundary.
func (s *rpcServer) startMCPOAuthLogin(requestID, id string) {
	s.startMCPOAuthLoginWith(requestID, id, func(ctx context.Context, serverID string) error {
		return mcpclient.Login(ctx, mcpclient.ConfigStore{Home: pkHome()}, serverID)
	})
}

func (s *rpcServer) startMCPOAuthLoginWith(requestID, id string, login func(context.Context, string) error) {
	s.startSkillOperation(requestID, "MCP login", "mcp_auth_status", "mcp_auth_status", map[string]any{"id": id, "status": "authorizing"}, func(ctx context.Context) (any, error) {
		if id == "" {
			return nil, errors.New("MCP server id is required")
		}
		if err := login(ctx, id); err != nil {
			// Don't propagate transport/server diagnostics: they can contain
			// arbitrary remote response text. The local status command provides
			// a safe next step without exposing OAuth material.
			return nil, errors.New("authorization did not complete; check `pk mcp status` and retry")
		}
		return map[string]any{"id": id, "status": "authenticated"}, nil
	})
}

func (s *rpcServer) mcpOAuthLogout(ctx context.Context, id string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errors.New("MCP server id is required")
	}
	store := mcpclient.ConfigStore{Home: pkHome()}
	servers, err := store.List()
	if err != nil {
		return nil, errors.New("could not read MCP server configuration")
	}
	for _, server := range servers {
		if server.ID != id {
			continue
		}
		if server.Auth.Mode != "oauth" {
			return nil, errors.New("MCP server is not configured for OAuth")
		}
		if err := store.ClearOAuthSession(server.Auth.SecretRef); err != nil {
			return nil, errors.New("could not clear the local OAuth session")
		}
		return map[string]any{"id": id, "status": "needs_login", "local_session_cleared": true}, nil
	}
	return nil, errors.New("MCP server is not configured")
}
