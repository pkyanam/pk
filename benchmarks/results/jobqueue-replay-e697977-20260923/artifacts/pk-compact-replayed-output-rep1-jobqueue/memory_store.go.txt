package jobqueue

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is a single-process reference store. A mutex makes each job
// transition atomic; order preserves FIFO behavior for ready jobs.
type MemoryStore struct {
	mu    sync.Mutex
	jobs  map[string]Job
	order []string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: make(map[string]Job)}
}

func (s *MemoryStore) Enqueue(ctx context.Context, job Job) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := job.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return false, nil
	}
	now := time.Now().UTC()
	job.State = StateReady
	job.Payload = append([]byte(nil), job.Payload...)
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	s.jobs[job.ID] = job
	s.order = append(s.order, job.ID)
	return true, nil
}

func (s *MemoryStore) Get(ctx context.Context, id string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[id]
	if !exists {
		return Job{}, ErrNotFound
	}
	return cloneJob(job), nil
}

func (s *MemoryStore) List(ctx context.Context) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]Job, 0, len(s.jobs))
	for _, id := range s.order {
		if job, exists := s.jobs[id]; exists {
			jobs = append(jobs, cloneJob(job))
		}
	}
	return jobs, nil
}

func (s *MemoryStore) Claim(ctx context.Context, now time.Time, lease time.Duration) (Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, false, err
	}
	if lease <= 0 {
		return Job{}, false, ErrInvalidJob
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.order {
		job, exists := s.jobs[id]
		if !exists || job.State != StateReady {
			continue
		}
		job.State = StateLeased
		job.Attempts++
		job.LeaseUntil = now.Add(lease)
		job.UpdatedAt = now
		s.jobs[id] = job
		return cloneJob(job), true, nil
	}
	return Job{}, false, nil
}

func (s *MemoryStore) Complete(ctx context.Context, id string, now time.Time) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[id]
	if !exists {
		return Job{}, ErrNotFound
	}
	if job.State != StateLeased {
		return Job{}, ErrNotLeased
	}
	job.State = StateCompleted
	job.LeaseUntil = time.Time{}
	job.LastError = ""
	job.UpdatedAt = now
	s.jobs[id] = job
	return cloneJob(job), nil
}

func (s *MemoryStore) Retry(ctx context.Context, id, cause string, now time.Time) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[id]
	if !exists {
		return Job{}, ErrNotFound
	}
	if job.State != StateLeased {
		return Job{}, ErrNotLeased
	}
	job.LastError = cause
	job.UpdatedAt = now
	job.LeaseUntil = time.Time{}
	if job.Attempts >= job.MaxAttempts {
		job.State = StateDead
	} else {
		job.State = StateReady
	}
	s.jobs[id] = job
	return cloneJob(job), nil
}
