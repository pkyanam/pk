// Package sessionlock coordinates runner and session-management access to a
// durable session. Lock files stay outside the recoverable session archive.
package sessionlock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrBusy = errors.New("session is currently in use")

// Lease is an exclusive advisory lease for one session ID. Call Release when
// all reads/writes or archive/restore operations are complete.
type Lease struct{ unlock func() error }

func (l *Lease) Release() error {
	if l == nil || l.unlock == nil {
		return nil
	}
	u := l.unlock
	l.unlock = nil
	return u()
}

// Acquire attempts a nonblocking exclusive lease. The persistent lock-file
// inode must not be deleted or moved while another process may hold it.
func Acquire(sessionDir, id string) (*Lease, error) {
	if strings.TrimSpace(sessionDir) == "" {
		return nil, errors.New("session directory is required for locking")
	}
	if !validID(id) {
		return nil, fmt.Errorf("invalid session ID %q", id)
	}
	root, err := filepath.Abs(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session directory: %w", err)
	}
	locks := filepath.Join(root, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return nil, fmt.Errorf("create session lock directory: %w", err)
	}
	if err := os.Chmod(locks, 0o700); err != nil {
		return nil, fmt.Errorf("secure session lock directory: %w", err)
	}
	sum := sha256.Sum256([]byte(id))
	path := filepath.Join(locks, hex.EncodeToString(sum[:])+".lock")
	unlock, busy, err := lockPath(path)
	if err != nil {
		return nil, fmt.Errorf("lock session %q: %w", id, err)
	}
	if busy {
		return nil, ErrBusy
	}
	return &Lease{unlock: unlock}, nil
}

func IsBusy(sessionDir, id string) (bool, error) {
	lease, err := Acquire(sessionDir, id)
	if errors.Is(err, ErrBusy) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, lease.Release()
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}
