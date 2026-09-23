package auth

import (
	"errors"
	"os"
)

func openLockFile(path string, requirePrivateMode bool) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("refusing symlinked credential lock file")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	after, statErr := os.Lstat(path)
	opened, fileErr := file.Stat()
	if statErr != nil || fileErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(after, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("credential lock file is not a regular file")
	}
	if requirePrivateMode && opened.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("credential lock file must have private permissions (0600)")
	}
	return file, nil
}
