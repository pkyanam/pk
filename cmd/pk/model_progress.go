package main

import (
	"context"

	"github.com/pkyanam/pk/internal/modelstream"
	"github.com/unreallabsai/unreal-agent/harness/llm"
)

// modelProgressAdapter installs a callback only for one foreground RPC turn.
// The inner adapter and its credential-refresh lifecycle remain shared, while
// request progress is correlated to this turn's RPC ID.
type modelProgressAdapter struct {
	inner   llm.Adapter
	observe func(modelstream.Event)
}

func (adapter modelProgressAdapter) Respond(ctx context.Context, request llm.Request, options llm.RequestOptions) (llm.Response, error) {
	return adapter.inner.Respond(modelstream.WithObserver(ctx, adapter.observe), request, options)
}
