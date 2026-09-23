package runner

// LifecycleEvent reports run boundaries and persisted provider responses. It
// intentionally contains no prompt, transcript, tool arguments, or tool output.
type LifecycleEvent struct {
	Type      string
	RunID     string
	SessionID string
	Model     string
	Workspace string
	Status    string
}

const (
	LifecycleRunStart         = "run_start"
	LifecycleResponseComplete = "response_complete"
	LifecycleRunEnd           = "run_end"
)
