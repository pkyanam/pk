package jobqueue

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidJob = errors.New("invalid job")
	ErrNotFound   = errors.New("job not found")
	ErrNotLeased  = errors.New("job is not leased")
)

type State string

const (
	StateReady     State = "ready"
	StateLeased    State = "leased"
	StateCompleted State = "completed"
	StateDead      State = "dead"
)

type Job struct {
	ID          string
	Payload     []byte
	State       State
	Attempts    int
	MaxAttempts int
	LeaseUntil  time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (j Job) Validate() error {
	if strings.TrimSpace(j.ID) == "" {
		return fmt.Errorf("%w: ID is required", ErrInvalidJob)
	}
	if j.MaxAttempts < 1 {
		return fmt.Errorf("%w: MaxAttempts must be positive", ErrInvalidJob)
	}
	if j.State != "" && j.State != StateReady && j.State != StateLeased && j.State != StateCompleted && j.State != StateDead {
		return fmt.Errorf("%w: unknown state %q", ErrInvalidJob, j.State)
	}
	if j.Attempts < 0 || j.Attempts > j.MaxAttempts {
		return fmt.Errorf("%w: attempts must be between zero and MaxAttempts", ErrInvalidJob)
	}
	return nil
}

func cloneJob(job Job) Job {
	job.Payload = append([]byte(nil), job.Payload...)
	return job
}
