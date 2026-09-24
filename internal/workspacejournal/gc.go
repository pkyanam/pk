package workspacejournal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// gcAsync starts one best-effort reference scan per Store. It does not delete
// objects or compact append-only journals.
func (s *Store) gcAsync() {
	s.gcMu.Lock()
	if s.gcStarted {
		s.gcMu.Unlock()
		return
	}
	s.gcStarted = true
	s.gcMu.Unlock()
	go func() {
		_ = s.gc()
	}()
}

// gc scans session journals to validate the object-reference set. It currently
// leaves both object files and append-only journals untouched.
func (s *Store) gc() error {
	sessionsDir := filepath.Join(s.root, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	sessionNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && validSessionID(entry.Name()) {
			sessionNames = append(sessionNames, entry.Name())
		}
	}
	sort.Strings(sessionNames)
	if len(sessionNames) > s.limits.MaxSessionsScanned {
		return fmt.Errorf("GC scan requires %d sessions, over the configured limit of %d; pass skipped", len(sessionNames), s.limits.MaxSessionsScanned)
	}
	referenced := map[string]bool{}
	scanFailed := false
	for _, session := range sessionNames {
		lock := s.lockSession(session)
		lock.mu.Lock()
		ops, _, foldErr := s.fold(session)
		if foldErr != nil {
			scanFailed = true
		}
		if foldErr == nil {
			for _, op := range ops {
				if op.Pre != nil {
					referenced[op.Pre.SHA256] = true
				}
				if op.Post != nil {
					referenced[op.Post.SHA256] = true
				}
				for _, restore := range op.Restores {
					if restore.PreSHA256 != "" {
						referenced[restore.PreSHA256] = true
					}
				}
			}
		}
		lock.mu.Unlock()
	}
	if scanFailed {
		// Any unreadable journal suppresses deletion for this pass.
		return errors.New("one or more journals could not be read; GC pass skipped")
	}
	return nil
}

// GCObjects is the explicit object-store garbage-collection entry point used
// by tests and the maintenance path. It removes unreferenced objects.
func (s *Store) GCObjects() (int, error) {
	sessionsDir := filepath.Join(s.root, "sessions")
	sessionEntries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var sessionNames []string
	for _, entry := range sessionEntries {
		if entry.IsDir() && validSessionID(entry.Name()) {
			sessionNames = append(sessionNames, entry.Name())
		}
	}
	sort.Strings(sessionNames)
	if len(sessionNames) > s.limits.MaxSessionsScanned {
		return 0, fmt.Errorf("GC scan requires %d sessions, over the configured limit of %d; no objects removed", len(sessionNames), s.limits.MaxSessionsScanned)
	}
	referenced := map[string]bool{}
	for _, session := range sessionNames {
		lock := s.lockSession(session)
		lock.mu.Lock()
		ops, _, err := s.fold(session)
		if err != nil {
			lock.mu.Unlock()
			return 0, fmt.Errorf("scan %s: %w", session, err)
		}
		lock.mu.Unlock()
		for _, op := range ops {
			if op.Pre != nil {
				referenced[op.Pre.SHA256] = true
			}
			if op.Post != nil {
				referenced[op.Post.SHA256] = true
			}
			for _, restore := range op.Restores {
				if restore.PreSHA256 != "" {
					referenced[restore.PreSHA256] = true
				}
			}
		}
	}
	removed := 0
	prefixes, err := os.ReadDir(filepath.Join(s.root, "objects"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	for _, prefix := range prefixes {
		if !prefix.IsDir() || len(prefix.Name()) != 2 {
			continue
		}
		objects, err := os.ReadDir(filepath.Join(s.root, "objects", prefix.Name()))
		if err != nil {
			continue
		}
		for _, object := range objects {
			name := object.Name()
			if object.IsDir() || len(name) != 64 {
				continue
			}
			valid := true
			for _, r := range name {
				if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			if referenced[name] {
				continue
			}
			if err := os.Remove(filepath.Join(s.root, "objects", prefix.Name(), name)); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// foldAsOf returns the journal entries at or before the given sequence
// cursor, used for cursor-relative summaries. An empty cursor selects
// everything retained.
func foldAsOf(ops []Op, cursor string) ([]Op, bool, error) {
	if strings.TrimSpace(cursor) == "" {
		return ops, true, nil
	}
	limit, err := strconv.ParseUint(strings.TrimSpace(cursor), 10, 64)
	if err != nil {
		return nil, false, fmt.Errorf("invalid cursor %q: expected a decimal sequence number", cursor)
	}
	filtered := make([]Op, 0, len(ops))
	for _, op := range ops {
		if op.Seq <= limit {
			filtered = append(filtered, op)
		}
	}
	return filtered, true, nil
}
