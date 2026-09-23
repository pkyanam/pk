package main

import (
	"github.com/pkyanam/pk/internal/websearch"
)

// tinyFishRegistryExtension is included in each run. Its decorator is a no-op
// unless TINYFISH_API_KEY is set, so unrelated sessions do not gain web tools.
func tinyFishRegistryExtension() cliRegistryExtension {
	config := websearch.Config{}
	return cliRegistryExtension{
		Decorate:          websearch.Decorator(config),
		RemoteJobHandlers: websearch.HandlerFactory(config),
	}
}
