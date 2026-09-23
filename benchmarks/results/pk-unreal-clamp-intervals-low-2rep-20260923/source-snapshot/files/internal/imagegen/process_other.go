//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package imagegen

import "os/exec"

func startProcessGroup(cmd *exec.Cmd) error { return cmd.Start() }
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
func waitProcessGroup(cmd *exec.Cmd) error { return cmd.Wait() }
