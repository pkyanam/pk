package main

import (
	"context"
	"errors"
)

func (s *rpcServer) webMutationAllowed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active || s.releaseActive || s.pluginCommandActive || s.skillOperationActive || s.attachedTask != "" || s.taskFollowCancel != nil || s.reloadPrepared {
		return errors.New("TinyFish credential changes require an idle session with no attached task")
	}
	return nil
}

func rpcWebStatusPayload() (map[string]any, error) {
	config, source, err := resolveWebSearchConfig(context.Background())
	if err != nil {
		return nil, errors.New("could not inspect TinyFish configuration")
	}
	configured := config.Backend != nil || config.APIKey != ""
	note := "Web tools are available to new sessions; credential validity has not been checked."
	if !configured {
		note = "Web tools are unavailable until a TinyFish key is configured."
	}
	return map[string]any{
		"configured":           configured,
		"source":               source,
		"tools_available":      configured,
		"environment_override": source == "environment",
		"note":                 note,
	}, nil
}
