//go:build windows

package tasks

import (
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
}
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(windows.Signal(0)) == nil
}
func signalProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
func lockFile(path string, blocking bool) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !blocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	ov := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ov)
	if err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ov); _ = f.Close() }, false, nil
}
func tryTaskLock(dir string) (bool, func(), error) {
	unlock, busy, err := lockFile(filepath.Join(dir, "worker.lock"), false)
	return busy, unlock, err
}
func lockTask(dir string) (func(), error) {
	unlock, _, err := lockFile(filepath.Join(dir, "worker.lock"), true)
	return unlock, err
}
func fileLock(path string) (func(), error) {
	unlock, _, err := lockFile(path, true)
	return unlock, err
}
func openInputLock(dir string) (func(), error) { return fileLock(filepath.Join(dir, "inputs.lock")) }
func openEventLock(dir string) (func(), error) { return fileLock(filepath.Join(dir, "events.lock")) }

var _ = time.Sleep

func tryLaunchLock(dir string) (bool, func(), error) {
	unlock, busy, err := lockFile(filepath.Join(dir, "launch.lock"), false)
	return busy, unlock, err
}

func syncDirectory(string) error { return nil }
