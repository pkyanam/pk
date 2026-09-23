//go:build windows

package extensions

import "os/exec"

// Windows currently guarantees termination of the worker executable. The
// portable v1 transport does not claim job-object isolation of its children.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
