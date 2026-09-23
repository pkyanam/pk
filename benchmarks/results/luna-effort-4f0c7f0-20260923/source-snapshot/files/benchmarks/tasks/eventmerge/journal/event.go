package journal

import "time"

type Event struct {
	Key      string
	Revision int
	At       time.Time
	Value    string
	Deleted  bool
}

// Merge returns one winning event for each key, ordered by key.
func Merge(existing, incoming []Event) []Event {
	return nil // TODO: choose the highest revision, then latest timestamp.
}
