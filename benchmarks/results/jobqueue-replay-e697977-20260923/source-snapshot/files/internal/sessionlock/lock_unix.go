//go:build unix

package sessionlock

import (
	"errors"
	"os"
	"syscall"
)

func lockPath(path string) (func() error, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err = f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, false, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		_ = f.Close()
		return nil, true, nil
	}
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	return func() error {
		unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		closeErr := f.Close()
		return errors.Join(unlockErr, closeErr)
	}, false, nil
}
