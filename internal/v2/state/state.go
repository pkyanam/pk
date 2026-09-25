// Package state implements the v2 State + transformations core.
//
// A State[T] holds a base value and an ordered list of registered
// transformations. Rebuild() resets the value to the base and applies every
// transformation once, in order, so the result is deterministic: the same
// base + same data + same transformation sequence always produce the same
// value. Reloads re-run the whole sequence from the base, so per-rebuild
// effects (like halving limits) never accumulate across rebuilds.
//
// The core safety invariant (see docs/v2-architecture.md) is that a State is
// never mutated in place: transformations must not mutate their input, and
// callers must not mutate a value returned by Value(). Every mutation happens
// inside Rebuild, which computes a fresh value and only then publishes it.
//
// The daemon is the single writer of every State, so State[T] needs no
// synchronization; cross-process races are excluded by design.
package state

import "fmt"

// Transform is a pure-ish transformation applied to a value. It should not
// mutate its input; it returns the transformed value, or an error.
type Transform[T any] func(T) (T, error)

// State is a value plus an ordered list of registered transformations.
type State[T any] struct {
	base       T
	transforms []Transform[T]
	value      T
	onUpdate   []func(T)
}

// New returns a State seeded with base (which for most States is the empty
// catalog, empty registry, etc.).
func New[T any](base T) *State[T] {
	return &State[T]{base: base, value: base}
}

// Add registers a transformation by appending it to the ordered list.
// Registration is append-only; the idempotency guarantee comes from the
// design: Rebuild always restarts from base and every registered transform
// must itself be deterministic, so registering (or re-running) the same
// transform never compounds its effect.
func (s *State[T]) Add(t Transform[T]) {
	if t == nil {
		return
	}
	s.transforms = append(s.transforms, t)
}

// Rebuild resets the value to base, applies each registered transformation
// once in order, and publishes the result to every OnUpdate subscriber.
// On error the previous value is left untouched and nothing is published.
func (s *State[T]) Rebuild() error {
	v := s.base
	for i, t := range s.transforms {
		next, err := t(v)
		if err != nil {
			return fmt.Errorf("state: transform %d: %w", i, err)
		}
		v = next
	}
	s.value = v
	for _, fn := range s.onUpdate {
		fn(v)
	}
	return nil
}

// Value returns the current value. The caller must not mutate it; treat it
// as read-only (a State is a single-writer value owned by the daemon).
func (s *State[T]) Value() T {
	return s.value
}

// OnUpdate registers a subscriber hook invoked with the new value after each
// successful Rebuild. This is how a value is published across sessions.
func (s *State[T]) OnUpdate(fn func(T)) {
	if fn != nil {
		s.onUpdate = append(s.onUpdate, fn)
	}
}
