package jobqueue

import (
	"context"
	"time"
)

// Store owns atomic state transitions. Implementations must return detached
// payload copies so callers cannot mutate persisted jobs through a result.
type Store interface {
	Enqueue(context.Context, Job) (bool, error)
	Get(context.Context, string) (Job, error)
	List(context.Context) ([]Job, error)
	Claim(context.Context, time.Time, time.Duration) (Job, bool, error)
	Complete(context.Context, string, time.Time) (Job, error)
	Retry(context.Context, string, string, time.Time) (Job, error)
	RecoverExpired(context.Context, time.Time) ([]Job, error)
}
