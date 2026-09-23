package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pkyanam/pk/internal/skillinstall"
)

func skillInstallManager() skillinstall.Manager {
	return skillinstall.Manager{Root: filepath.Join(pkHome(), "skills")}
}

func (s *rpcServer) skillMutationAllowed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared {
		return errors.New("skill changes require an idle session with no attached task")
	}
	return nil
}

func (s *rpcServer) startSkillOperation(requestID, operationName, startedEvent, resultEvent string, startedPayload map[string]any, run func(context.Context) (any, error)) {
	s.mu.Lock()
	if s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared {
		s.mu.Unlock()
		_ = s.emit(requestID, "error", map[string]any{"message": operationName + " operations require an idle session with no attached task", "recoverable": true})
		return
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.skillOperationActive = true
	s.skillOperationCancel = cancel
	s.skillOperationDone = done
	s.skillOperationKind = operationName
	s.mu.Unlock()
	_ = s.emit(requestID, startedEvent, startedPayload)
	go func() {
		result, err := run(ctx)
		canceled := ctx.Err() != nil
		cancel()
		s.mu.Lock()
		s.skillOperationActive = false
		s.skillOperationCancel = nil
		s.skillOperationKind = ""
		s.mu.Unlock()
		if err != nil {
			_ = s.emit(requestID, "error", map[string]any{"message": fmt.Sprintf("%s operation failed: %v", operationName, err), "recoverable": true, "cancelled": canceled})
		} else {
			_ = s.emit(requestID, resultEvent, result)
		}
		close(done)
		s.mu.Lock()
		if s.skillOperationDone == done {
			s.skillOperationDone = nil
		}
		s.mu.Unlock()
	}()
}

func (s *rpcServer) cancelRPCOperation(requestID, kindPrefix, event string) {
	s.mu.Lock()
	cancel, kind := s.skillOperationCancel, s.skillOperationKind
	active := s.skillOperationActive && cancel != nil && strings.HasPrefix(kind, kindPrefix)
	s.mu.Unlock()
	if !active {
		_ = s.emit(requestID, "error", map[string]any{"message": "no matching background operation is running", "recoverable": true})
		return
	}
	cancel()
	_ = s.emit(requestID, event, map[string]any{})
}
