//go:build windows

package auth

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func acquireRefreshLock(ctx context.Context, authFile string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := openLockFile(filepath.Clean(authFile)+".lock", false)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	for {
		if err := ctx.Err(); err != nil {
			_ = lock.Close()
			return nil, err
		}
		err = windows.LockFileEx(windows.Handle(lock.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			return func() {
				_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, overlapped)
				_ = lock.Close()
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = lock.Close()
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = lock.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
