//go:build windows

package sessionlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
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
	ov := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ov)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		_ = f.Close()
		return nil, true, nil
	}
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	return func() error {
		unlockErr := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ov)
		closeErr := f.Close()
		return errors.Join(unlockErr, closeErr)
	}, false, nil
}
