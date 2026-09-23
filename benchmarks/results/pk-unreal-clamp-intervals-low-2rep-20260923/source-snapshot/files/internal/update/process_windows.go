//go:build windows

package update

import (
	"context"
	"os/exec"
	"time"
)

func runProcessTree(ctx context.Context, cmd *exec.Cmd) error {
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}
