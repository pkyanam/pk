//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// The fallback uses an exclusive lock file on uncommon targets. Unix and
// Windows builds use kernel-managed locks that are released on process exit.
func acquireRefreshLock(ctx context.Context, authFile string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Clean(authFile) + ".lock"
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = lock.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
