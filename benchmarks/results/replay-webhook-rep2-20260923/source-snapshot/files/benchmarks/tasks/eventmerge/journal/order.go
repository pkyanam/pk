package journal

func newer(candidate, current Event) bool {
	return false // TODO: compare revision and timestamp; preserve stable ties.
}
