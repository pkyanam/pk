package runner

import "strings"

// correctSchedulingPreamble removes the upstream heartbeat promise, which is
// not enabled by pk's coordinator, and explains the actual idle/resume behavior.
func correctSchedulingPreamble(prompt string) string {
	prompt = strings.Replace(prompt,
		"You never have to babysit a running call: harness does it for you. As a backup, if calls are active and nothing has happened for ten minutes, a heartbeat wakes you, and this is an opportunity to check that all is well.",
		"When calls are running, ending a turn without new calls lets the harness wait for their results. The harness does not schedule periodic heartbeat turns.", 1)
	prompt = strings.Replace(prompt,
		"Ending a turn with no tool calls while calls are running means you sleep until one finishes; ending a turn with nothing running ends the session, so do that only when the task is complete.",
		"When no calls are running and your work is complete, finish your reply. pk returns control and saves the session so it can be resumed later.", 1)
	return prompt
}
