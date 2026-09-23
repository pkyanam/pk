package jobqueue

import (
	"context"
	"time"
)

// RecoverExpired moves expired leases back to ready, unless the job has
// exhausted MaxAttempts, in which case the job becomes dead.
func (s *MemoryStore) RecoverExpired(ctx context.Context, now time.Time) ([]Job, error) {
	return nil, nil // TODO: transition expired leased jobs atomically.
}
