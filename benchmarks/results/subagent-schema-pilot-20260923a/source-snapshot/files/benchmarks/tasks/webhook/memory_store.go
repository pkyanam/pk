package webhook

import (
	"context"
	"sync"
)

// Store atomically inserts an event if its ID has not been seen. It returns
// true only for the first successful insertion.
type Store interface {
	InsertIfAbsent(context.Context, Event) (bool, error)
}

// MemoryStore is a concurrency-safe process-local implementation of Store.
type MemoryStore struct {
	mu     sync.Mutex
	events map[string]Event
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{events: make(map[string]Event)}
}

func (store *MemoryStore) InsertIfAbsent(ctx context.Context, event Event) (bool, error) {
	// TODO: Respect cancellation, make deduplication atomic, and copy the payload
	// so later caller mutation cannot change the stored event.
	return false, nil
}

// Get returns a copy of the stored event, if present.
func (store *MemoryStore) Get(id string) (Event, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	event, ok := store.events[id]
	if ok {
		event.Payload = append(event.Payload[:0:0], event.Payload...)
	}
	return event, ok
}
