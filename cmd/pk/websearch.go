package main

import (
	"github.com/pkyanam/pk/internal/websearch"
)

// tinyFishRegistryExtension is included in each run. Its decorator is a no-op
// unless the user configured an environment or private-store credential.
func tinyFishRegistryExtension() cliRegistryExtension {
	key, _, _ := websearch.ResolveAPIKey(pkHome())
	config := websearch.Config{APIKey: key}
	return cliRegistryExtension{
		Decorate:          websearch.Decorator(config),
		RemoteJobHandlers: websearch.HandlerFactory(config),
	}
}
