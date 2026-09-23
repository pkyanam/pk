//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package imagegen

import (
	"os/exec"
	"syscall"
)

func startProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func waitProcessGroup(cmd *exec.Cmd) error { return cmd.Wait() }
