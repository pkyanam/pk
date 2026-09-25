package runner

import (
	"strings"
	"testing"
)

func TestCorrectSchedulingPreamble(t *testing.T) {
	const upstream = "before\nYou never have to babysit a running call: harness does it for you. As a backup, if calls are active and nothing has happened for ten minutes, a heartbeat wakes you, and this is an opportunity to check that all is well.\nEnding a turn with no tool calls while calls are running means you sleep until one finishes; ending a turn with nothing running ends the session, so do that only when the task is complete.\nafter"
	got := correctSchedulingPreamble(upstream)
	for _, want := range []string{
		"before", "after", "lets the harness wait for their results",
		"does not schedule periodic heartbeat turns", "saves the session so it can be resumed later",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("corrected prompt missing %q: %s", want, got)
		}
	}
	for _, stale := range []string{"ten minutes", "heartbeat wakes you", "ends the session"} {
		if strings.Contains(got, stale) {
			t.Errorf("corrected prompt retains stale wording %q: %s", stale, got)
		}
	}
}
