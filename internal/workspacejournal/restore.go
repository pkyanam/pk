package workspacejournal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxRestoreFingerprintBytes bounds how much of the current file is read to
// compute the before-restore fingerprint. Larger current files are refused
// rather than partially fingerprinted.
const maxRestoreFingerprintBytes = 64 << 20

// maxSnapshotFiles bounds how many files one safety snapshot materializes.
const maxSnapshotFiles = 4096

// RestoreAction is one planned, reversible change.
type RestoreAction struct {
	OpID   string `json:"op_id"`
	Path   string `json:"path"`
	Kind   string `json:"kind"` // write or delete
	ToSHA  string `json:"to_sha256,omitempty"`
	FromSHA string `json:"from_sha256,omitempty"`
}

// RestoreReport describes one executed restore.
type RestoreReport struct {
	Snapshot    string           `json:"snapshot"`
	SnapshotDir string           `json:"snapshot_dir,omitempty"`
	Restored    []string         `json:"restored"`
	Deleted     []string         `json:"deleted"`
	Skipped     []SkippedRestore `json:"skipped"`
}

// SkippedRestore explains why one selected operation was not restored.
type SkippedRestore struct {
	OpID   string `json:"op_id"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// List returns the folded, retained journal entries for one session.
func (s *Store) List(session string) ([]Op, error) {
	if !validSessionID(session) {
		return nil, errors.New("journal session ID must be 32 hexadecimal characters")
	}
	lock := s.lockSession(session)
	lock.mu.Lock()
	defer lock.mu.Unlock()
	ops, _, err := s.fold(session)
	return ops, err
}

// selectOps chooses operations to restore. Explicit IDs restore exactly those
// operations; an empty selection restores every completed or restored mutation
// that still has a stored preimage and is not superseded by a later recorded
// mutation of the same path.
func selectOps(ops []Op, ids []string) ([]Op, []SkippedRestore, error) {
	if len(ids) == 0 {
		var selected []Op
		lastWrite := map[string]int{}
		for index, op := range ops {
			if op.Status == StatusCompleted || op.Status == StatusRestored {
				lastWrite[op.Path] = index
			}
		}
		for index, op := range ops {
			if op.Pre == nil {
				continue
			}
			if op.Status != StatusCompleted && op.Status != StatusRestored {
				continue
			}
			if lastWrite[op.Path] != index {
				continue // superseded by a later recorded mutation
			}
			selected = append(selected, op)
		}
		return selected, nil, nil
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	var selected []Op
	found := map[string]bool{}
	var skipped []SkippedRestore
	for _, op := range ops {
		if !wanted[op.ID] {
			continue
		}
		found[op.ID] = true
		if op.Pre == nil {
			skipped = append(skipped, SkippedRestore{OpID: op.ID, Path: op.Path, Reason: "no stored preimage (the file did not exist before this mutation)"})
			continue
		}
		if op.Status != StatusCompleted && op.Status != StatusRestored {
			skipped = append(skipped, SkippedRestore{OpID: op.ID, Path: op.Path, Reason: "operation did not complete; nothing to undo"})
			continue
		}
		selected = append(selected, op)
	}
	for _, id := range ids {
		if !found[id] {
			return nil, nil, fmt.Errorf("unknown journal operation %q", id)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Seq < selected[j].Seq })
	return selected, skipped, nil
}

// PlanRestore computes the changes a restore would make without touching the
// workspace.
func (s *Store) PlanRestore(ctx context.Context, session, workspace string, ids []string) ([]RestoreAction, []SkippedRestore, error) {
	ops, err := s.List(session)
	if err != nil {
		return nil, nil, err
	}
	selected, skipped, err := selectOps(ops, ids)
	if err != nil {
		return nil, nil, err
	}
	root, err := resolveWorkspaceRoot(workspace)
	if err != nil {
		return nil, nil, err
	}
	var actions []RestoreAction
	for _, op := range selected {
		current, currentExists, err := currentFileDigest(root, op.Path)
		if err != nil {
			skipped = append(skipped, SkippedRestore{OpID: op.ID, Path: op.Path, Reason: errorMessage(err)})
			continue
		}
		if op.Pre == nil {
			continue
		}
		if currentExists {
			actions = append(actions, RestoreAction{OpID: op.ID, Path: op.Path, Kind: "write", ToSHA: op.Pre.SHA256, FromSHA: current.SHA256})
		} else {
			// The file pk created is already gone; restoring its preimage is
			// meaningless and deletion would be out of scope for a plan.
			skipped = append(skipped, SkippedRestore{OpID: op.ID, Path: op.Path, Reason: "file is currently missing; nothing to restore onto"})
		}
	}
	return actions, skipped, nil
}

func resolveWorkspaceRoot(workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		return "", errors.New("workspace is required")
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("workspace %q is not a directory", workspace)
	}
	return resolved, nil
}

// currentFileDigest reads the current workspace file and returns its digest.
func currentFileDigest(root, slashPath string) (Content, bool, error) {
	path := filepath.Join(root, filepath.FromSlash(slashPath))
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Content{}, false, nil
	}
	if err != nil {
		return Content{}, false, fmt.Errorf("inspect %s: %w", slashPath, err)
	}
	if !info.Mode().IsRegular() {
		return Content{}, true, fmt.Errorf("%s is not a regular file now; refusing to restore over it", slashPath)
	}
	if info.Size() > maxRestoreFingerprintBytes {
		return Content{}, true, fmt.Errorf("%s exceeds the restore fingerprint limit", slashPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Content{}, true, fmt.Errorf("read %s: %w", slashPath, err)
	}
	digest := sha256.Sum256(data)
	return Content{SHA256: hex.EncodeToString(digest[:]), Size: len(data)}, true, nil
}

// Restore reverts the selected (or, by default, all non-superseded) recorded
// mutations of one session. Before any change, the current content of every
// affected file is snapshotted for safety. Each applied change is recorded in
// the journal with the snapshot reference.
func (s *Store) Restore(ctx context.Context, session, workspace string, ids []string) (RestoreReport, error) {
	report := RestoreReport{}
	ops, err := s.List(session)
	if err != nil {
		return report, err
	}
	selected, skipped, err := selectOps(ops, ids)
	if err != nil {
		return report, err
	}
	root, err := resolveWorkspaceRoot(workspace)
	if err != nil {
		return report, err
	}
	lock := s.lockSession(session)
	lock.mu.Lock()
	defer lock.mu.Unlock()

	// Re-read current state under the session lock so the fingerprint checks
	// and the safety snapshot cover the same instant.
	var targets []snapshotTarget
	for _, op := range selected {
		current, exists, err := currentFileDigest(root, op.Path)
		if err != nil {
			skipped = append(skipped, SkippedRestore{OpID: op.ID, Path: op.Path, Reason: errorMessage(err)})
			continue
		}
		if op.Pre == nil {
			continue
		}
		if !exists {
			skipped = append(skipped, SkippedRestore{OpID: op.ID, Path: op.Path, Reason: "file is currently missing; nothing to restore onto"})
			continue
		}
		expected := ""
		if op.Post != nil {
			expected = op.Post.SHA256
		}
		currentMatchesPreimage := current.SHA256 == op.Pre.SHA256
		if expected != "" && !currentMatchesPreimage && current.SHA256 != expected {
			// The file is neither the recorded postimage (an undo candidate)
			// nor the recorded preimage (an already-restored state): it holds
			// unrecognized content, so refuse rather than overwrite it.
			skipped = append(skipped, SkippedRestore{OpID: op.ID, Path: op.Path, Reason: fmt.Sprintf("file changed since this mutation (%s now, %s expected); refusing without a matching state", shortDigest(current.SHA256), shortDigest(expected))})
			continue
		}
		mode := os.FileMode(0o644)
		if info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(op.Path))); statErr == nil {
			mode = info.Mode().Perm()
		}
		targets = append(targets, snapshotTarget{op: op, current: current, mode: mode})
	}
	if len(targets) == 0 {
		report.Skipped = skipped
		return report, nil
	}

	snapshotName, err := s.createSnapshot(session, root, targetPaths(targets))
	if err != nil {
		return report, fmt.Errorf("create pre-restore snapshot: %w", err)
	}
	report.Snapshot = snapshotName
	report.SnapshotDir = filepath.Join(s.sessionDir(session), "restores", snapshotName)

	applied := 0
	for _, item := range targets {
		preBytes, err := s.readObject(*item.op.Pre)
		if err != nil {
			skipped = append(skipped, SkippedRestore{OpID: item.op.ID, Path: item.op.Path, Reason: errorMessage(err)})
			continue
		}
		// The file may have changed between snapshot and apply; refuse rather
		// than overwrite a state the snapshot did not capture.
		again, exists, err := currentFileDigest(root, item.op.Path)
		if err != nil || !exists || again.SHA256 != item.current.SHA256 {
			skipped = append(skipped, SkippedRestore{OpID: item.op.ID, Path: item.op.Path, Reason: "file changed during restore; skipped"})
			continue
		}
		if err := atomicRestoreWrite(root, item.op.Path, preBytes, item.mode); err != nil {
			skipped = append(skipped, SkippedRestore{OpID: item.op.ID, Path: item.op.Path, Reason: errorMessage(err)})
			continue
		}
		note := restoreNote{PreSHA256: item.op.Pre.SHA256, SnapshotRef: snapshotName}
		line := preparedLine{Kind: "restore", ID: item.op.ID, Restore: &note, Reason: "restored by user", Time: time.Now().UTC()}
		if err := s.appendLine(session, line); err != nil {
			skipped = append(skipped, SkippedRestore{OpID: item.op.ID, Path: item.op.Path, Reason: "restored but the journal could not record it: " + errorMessage(err)})
			continue
		}
		report.Restored = append(report.Restored, item.op.ID)
		applied++
	}
	_ = applied
	report.Skipped = append(skipped, report.Skipped...)
	s.gcAsync()
	return report, nil
}

type snapshotTarget struct {
	op      Op
	current Content
	mode    os.FileMode
}

func targetPaths(targets []snapshotTarget) []string {
	paths := make([]string, 0, len(targets))
	for _, item := range targets {
		paths = append(paths, item.op.Path)
	}
	return paths
}

func shortDigest(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

// atomicRestoreWrite replaces a workspace file with restored content using a
// temporary file and rename. It never follows symlinks: an intermediate
// symlink causes an error instead of an escape.
func atomicRestoreWrite(root, slashPath string, contents []byte, mode os.FileMode) error {
	absolute := filepath.Join(root, filepath.FromSlash(slashPath))
	if err := lstatRegularFile(absolute); err != nil {
		return err
	}
	dir := filepath.Dir(absolute)
	temp, err := os.CreateTemp(dir, ".pk-restore-*")
	if err != nil {
		return fmt.Errorf("create restore temp file: %w", err)
	}
	name := temp.Name()
	writeErr := func() error {
		if err := temp.Chmod(mode); err != nil {
			return err
		}
		if _, err := temp.Write(contents); err != nil {
			return err
		}
		return temp.Sync()
	}()
	closeErr := temp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = os.Rename(name, absolute)
	}
	if writeErr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("restore %s: %w", slashPath, writeErr)
	}
	_ = syncDir(dir)
	return nil
}

func lstatRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("target is not a regular file")
	}
	return nil
}

// createSnapshot materializes the current content of the listed workspace
// paths into a new restores/ directory. Content objects are shared with the
// object store via a hardlink when possible, with a copy fallback.
func (s *Store) createSnapshot(session, workspace string, paths []string) (string, error) {
	if len(paths) > maxSnapshotFiles {
		return "", fmt.Errorf("snapshot would cover %d files, over the limit of %d", len(paths), maxSnapshotFiles)
	}
	base, err := newSnapshotName()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.sessionDir(session), "restores", base)
	if err := os.MkdirAll(dir, objectDirPerm); err != nil {
		return "", fmt.Errorf("create snapshot directory: %w", err)
	}
	type snapshotFile struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	}
	var files []snapshotFile
	for _, slashPath := range paths {
		absolute := filepath.Join(workspace, filepath.FromSlash(slashPath))
		info, err := os.Lstat(absolute)
		if err != nil {
			return "", fmt.Errorf("snapshot %s: %w", slashPath, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("snapshot %s: not a regular file", slashPath)
		}
		if info.Size() > maxRestoreFingerprintBytes {
			return "", fmt.Errorf("snapshot %s: exceeds the restore fingerprint limit", slashPath)
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			return "", fmt.Errorf("snapshot %s: %w", slashPath, err)
		}
		content, err := s.writeObject(data)
		if err != nil {
			return "", fmt.Errorf("snapshot %s: %w", slashPath, err)
		}
		link := filepath.Join(dir, fmt.Sprintf("%02d", len(files)))
		if err := os.Link(filepath.Join(s.root, "objects", content.SHA256[:2], content.SHA256), link); err != nil {
			file, copyErr := os.Create(link)
			if copyErr != nil {
				return "", fmt.Errorf("snapshot %s: %w", slashPath, copyErr)
			}
			if _, copyErr := file.Write(data); copyErr != nil {
				file.Close()
				return "", fmt.Errorf("snapshot %s: %w", slashPath, copyErr)
			}
			if copyErr := file.Close(); copyErr != nil {
				return "", fmt.Errorf("snapshot %s: %w", slashPath, copyErr)
			}
		}
		files = append(files, snapshotFile{Path: slashPath, SHA256: content.SHA256, Size: content.Size})
	}
	manifest := struct {
		Version int            `json:"version"`
		Time    string         `json:"time"`
		Files   []snapshotFile `json:"files"`
	}{Version: 1, Time: time.Now().UTC().Format(time.RFC3339Nano), Files: files}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), filePerm); err != nil {
		return "", fmt.Errorf("write snapshot manifest: %w", err)
	}
	_ = syncDir(dir)
	return base, nil
}

func newSnapshotName() (string, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	stamp := strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "-")
	return fmt.Sprintf("%s-%s", stamp, hex.EncodeToString(raw[:])), nil
}
