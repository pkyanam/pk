//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package auth

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"time"
)

func acquireRefreshLock(ctx context.Context, authFile string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := openLockFile(filepath.Clean(authFile)+".lock", true)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = lock.Close()
			return nil, err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				_ = lock.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
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
