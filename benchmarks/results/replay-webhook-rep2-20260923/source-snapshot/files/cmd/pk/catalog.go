package main

import (
	"context"
	"os"

	"github.com/pkyanam/pk/internal/runner"
)

func (server *rpcServer) skillCatalog(ctx context.Context) (runner.SkillCatalog, error) {
	server.mu.Lock()
	sessionDir, sessionID, workspace := server.sessionDir, server.session, server.opts.Workspace
	dirs := append([]string(nil), server.opts.SkillsDirs...)
	server.mu.Unlock()
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return runner.SkillCatalog{}, err
		}
	}
	if len(dirs) == 0 {
		dirs = defaultSkillDirs()
	}
	return runner.LoadSkillCatalog(ctx, sessionDir, sessionID, dirs, workspace)
}

func (server *rpcServer) readSkill(ctx context.Context, name string) (runner.SkillDocument, error) {
	server.mu.Lock()
	sessionDir, sessionID, workspace := server.sessionDir, server.session, server.opts.Workspace
	dirs := append([]string(nil), server.opts.SkillsDirs...)
	server.mu.Unlock()
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return runner.SkillDocument{}, err
		}
	}
	if len(dirs) == 0 {
		dirs = defaultSkillDirs()
	}
	return runner.ReadSkill(ctx, sessionDir, sessionID, dirs, workspace, name)
}
