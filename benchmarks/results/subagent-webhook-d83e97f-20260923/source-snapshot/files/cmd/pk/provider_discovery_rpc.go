package main

import (
	"context"
	"errors"
	"time"

	"github.com/pkyanam/pk/internal/providers"
)

type providerModelsRequest struct {
	RequestID string
	Provider  providers.Provider
}

// startProviderModels serializes model discovery to one request at a time. A
// newer request cancels the active fetch and replaces the single pending slot,
// so rapid picker changes cannot accumulate HTTP goroutines.
func (s *rpcServer) startProviderModels(request providerModelsRequest) {
	_ = s.emit(request.RequestID, "provider_models_started", map[string]any{"provider_id": request.Provider.ID})
	var cancelledActive, cancelledPending string
	s.mu.Lock()
	if s.providerModelsCancel != nil {
		cancelledActive = s.providerModelsRequestID
		s.providerModelsCancel()
		if s.pendingProviderModels != nil {
			cancelledPending = s.pendingProviderModels.RequestID
		}
		s.pendingProviderModels = &request
		s.mu.Unlock()
		if cancelledActive != "" {
			_ = s.emit(cancelledActive, "provider_models_cancelled", map[string]any{"request_id": cancelledActive})
		}
		if cancelledPending != "" {
			_ = s.emit(cancelledPending, "provider_models_cancelled", map[string]any{"request_id": cancelledPending})
		}
		return
	}
	s.launchProviderModelsLocked(request)
	s.mu.Unlock()
}

func (s *rpcServer) launchProviderModelsLocked(request providerModelsRequest) {
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	s.providerModelsRequestID = request.RequestID
	s.providerModelsCancel = cancel
	go s.runProviderModels(ctx, cancel, request)
}

func (s *rpcServer) runProviderModels(ctx context.Context, cancel context.CancelFunc, request providerModelsRequest) {
	defer cancel()
	models, err := request.Provider.Models(ctx)
	s.mu.Lock()
	if s.providerModelsRequestID != request.RequestID {
		s.mu.Unlock()
		return
	}
	s.providerModelsRequestID = ""
	s.providerModelsCancel = nil
	pending := s.pendingProviderModels
	s.pendingProviderModels = nil
	if pending != nil {
		s.launchProviderModelsLocked(*pending)
	}
	s.mu.Unlock()

	if !errors.Is(ctx.Err(), context.Canceled) {
		if err != nil {
			_ = s.emit(request.RequestID, "error", map[string]any{"message": err.Error(), "recoverable": true})
		} else {
			_ = s.emit(request.RequestID, "provider_models", map[string]any{"provider_id": request.Provider.ID, "models": models})
		}
	}
}

func (s *rpcServer) cancelProviderModels(requestID string) error {
	if requestID == "" {
		return errors.New("request_id is required")
	}
	s.mu.Lock()
	found := false
	if s.providerModelsRequestID == requestID && s.providerModelsCancel != nil {
		s.providerModelsCancel()
		found = true
	}
	if s.pendingProviderModels != nil && s.pendingProviderModels.RequestID == requestID {
		s.pendingProviderModels = nil
		found = true
	}
	s.mu.Unlock()
	if !found {
		return errors.New("provider model discovery request is no longer active")
	}
	return nil
}
