package main

import (
	"context"
	"time"

	"github.com/pkyanam/pk/internal/websearch"
)

// tinyFishRegistryExtension is included in each run. Its decorator is a no-op
// unless the user configured an environment or private-store credential.
func tinyFishRegistryExtension(ctx context.Context) cliRegistryExtension {
	config, _, _ := resolveWebSearchConfig(ctx)
	return cliRegistryExtension{
		Decorate:          websearch.Decorator(config),
		RemoteJobHandlers: websearch.HandlerFactory(config),
	}
}

// resolveWebSearchConfig prefers an explicitly configured direct TinyFish key.
// If absent, it may use the user's installed Monid CLI, which keeps its API key
// in Monid's own credential store. No key is copied into pk or subprocess args.
func resolveWebSearchConfig(parent context.Context) (websearch.Config, string, error) {
	if parent == nil {
		parent = context.Background()
	}
	key, source, err := websearch.ResolveAPIKey(pkHome())
	if err != nil {
		return websearch.Config{}, "none", err
	}
	if key != "" {
		return websearch.Config{APIKey: key}, source, nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	monid, available, err := websearch.MonidCLIClientFromPATH(ctx)
	if err != nil {
		return websearch.Config{}, "none", err
	}
	if available {
		return websearch.Config{Backend: monid}, "monid_cli", nil
	}
	return websearch.Config{}, "none", nil
}
