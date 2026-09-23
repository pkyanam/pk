//go:build !windows

package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Build scripts spawn compilers and test processes. Cancel their whole process
// group, including descendants retaining output pipes, before deleting staging.
func runProcessTree(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}
