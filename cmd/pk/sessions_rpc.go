package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/sessionmanager"
)

const (
	rpcSessionPageLimit = 200
	rpcSessionMaxLimit  = 500
	rpcSessionMaxBatch  = 100
)

func rpcSessionsManager(sessionDir string) sessionmanager.Manager {
	return sessionmanager.Manager{SessionDir: sessionDir}
}

func (s *rpcServer) rpcActiveSessionIDs() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make(map[string]bool, 1)
	// Keep the session currently open in this UI out of the archive/restore
	// path even between turns. Other processes are covered by session leases.
	if s.session != "" {
		active[s.session] = true
	}
	return active
}

func (s *rpcServer) listSessions(ctx context.Context, query, workspace string, limit int, kind string) ([]sessionmanager.Session, error) {
	if len(query) > 512 || len(workspace) > 4096 {
		return nil, errors.New("session search fields exceed their size limits")
	}
	if limit <= 0 {
		limit = rpcSessionPageLimit
	}
	if limit > rpcSessionMaxLimit {
		return nil, errors.New("session list limit exceeds 500")
	}
	if kind != "" && kind != "main" && kind != "subagents" {
		return nil, errors.New("session kind must be main or subagents")
	}
	s.mu.Lock()
	sessionDir := s.sessionDir
	s.mu.Unlock()
	return rpcSessionsManager(sessionDir).List(ctx, sessionmanager.ListOptions{
		Query: query, Workspace: filepath.Clean(strings.TrimSpace(workspace)), Kind: kind,
		ActiveIDs: s.rpcActiveSessionIDs(), Limit: limit,
	})
}

func (s *rpcServer) getSession(ctx context.Context, id string) (sessionmanager.Session, error) {
	s.mu.Lock()
	sessionDir := s.sessionDir
	activeIDs := make(map[string]bool, 1)
	if s.session != "" {
		activeIDs[s.session] = true
	}
	s.mu.Unlock()
	return rpcSessionsManager(sessionDir).Get(ctx, id, activeIDs)
}

func (s *rpcServer) archiveSessions(ctx context.Context, ids []string) []sessionmanager.Result {
	if len(ids) == 0 || len(ids) > rpcSessionMaxBatch {
		return []sessionmanager.Result{{Error: "select between 1 and 100 sessions"}}
	}
	s.mu.Lock()
	sessionDir := s.sessionDir
	s.mu.Unlock()
	return rpcSessionsManager(sessionDir).Archive(ctx, ids, s.rpcActiveSessionIDs())
}

func (s *rpcServer) listSessionTrash(ctx context.Context, query string) ([]sessionmanager.TrashEntry, error) {
	if len(query) > 512 {
		return nil, errors.New("trash search query exceeds 512 bytes")
	}
	s.mu.Lock()
	sessionDir := s.sessionDir
	s.mu.Unlock()
	entries, err := rpcSessionsManager(sessionDir).ListTrash(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(entries) > rpcSessionMaxLimit {
		entries = entries[:rpcSessionMaxLimit]
	}
	return entries, nil
}

func (s *rpcServer) restoreSessions(ctx context.Context, trashIDs []string) []sessionmanager.Result {
	if len(trashIDs) == 0 || len(trashIDs) > rpcSessionMaxBatch {
		return []sessionmanager.Result{{Error: "select between 1 and 100 archived sessions"}}
	}
	s.mu.Lock()
	sessionDir := s.sessionDir
	active := make(map[string]bool, 1)
	if s.session != "" {
		active[s.session] = true
	}
	s.mu.Unlock()
	return rpcSessionsManager(sessionDir).Restore(ctx, trashIDs, active)
}

func (s *rpcServer) purgeSessions(ctx context.Context, trashIDs []string) []sessionmanager.Result {
	if len(trashIDs) == 0 || len(trashIDs) > rpcSessionMaxBatch {
		return []sessionmanager.Result{{Error: "select between 1 and 100 archived sessions"}}
	}
	s.mu.Lock()
	sessionDir := s.sessionDir
	active := make(map[string]bool, 1)
	if s.session != "" {
		active[s.session] = true
	}
	s.mu.Unlock()
	return rpcSessionsManager(sessionDir).Purge(ctx, trashIDs, active)
}
