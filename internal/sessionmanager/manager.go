// Package sessionmanager provides searchable session metadata and recoverable
// archive/restore operations over the local durable session store.
package sessionmanager

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
	"unicode"

	"github.com/pkyanam/pk/internal/attachments"
	"github.com/pkyanam/pk/internal/presentation"
	"github.com/pkyanam/pk/internal/sessionlock"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

const (
	defaultPreviewRunes = 320
	archiveVersion      = 1
	maxMetadataItems    = 100000
)

type Manager struct {
	SessionDir string
	TrashDir   string
}

type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Workspace string    `json:"workspace,omitempty"`
	Title     string    `json:"title,omitempty"`
	Preview   string    `json:"preview,omitempty"`
	ItemCount int       `json:"item_count"`
	Active    bool      `json:"active"`
}

type ListOptions struct {
	Query     string
	Workspace string
	ActiveIDs map[string]bool
	Limit     int
}

type TrashEntry struct {
	TrashID    string    `json:"trash_id"`
	SessionID  string    `json:"session_id"`
	ArchivedAt time.Time `json:"archived_at"`
	Workspace  string    `json:"workspace,omitempty"`
	Title      string    `json:"title,omitempty"`
	State      string    `json:"state"`
}

type Result struct {
	SessionID string `json:"session_id"`
	TrashID   string `json:"trash_id,omitempty"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

type archiveManifest struct {
	Version    int       `json:"version"`
	TrashID    string    `json:"trash_id"`
	SessionID  string    `json:"session_id"`
	ArchivedAt time.Time `json:"archived_at"`
	Workspace  string    `json:"workspace,omitempty"`
	Title      string    `json:"title,omitempty"`
	State      string    `json:"state"`
	Files      []string  `json:"files"`
}

type contextMetadata struct {
	Workspace string
}

// Archive moves the selected session and its harness-managed sidecars into
// recoverable trash. It refuses caller-reported active sessions and sessions
// whose process-wide lease is held by another current pk process.
func (m Manager) Archive(ctx context.Context, ids []string, activeIDs map[string]bool) []Result {
	results := make([]Result, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		result := Result{SessionID: id}
		if err := ctx.Err(); err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		if seen[id] {
			result.Error = "duplicate session ID in request"
			results = append(results, result)
			continue
		}
		seen[id] = true
		if activeIDs[id] {
			result.Error = sessionlock.ErrBusy.Error()
			results = append(results, result)
			continue
		}
		result = m.archiveOne(ctx, id)
		results = append(results, result)
	}
	return results
}

func (m Manager) archiveOne(ctx context.Context, id string) Result {
	result := Result{SessionID: id}
	sessionsDir, trashDir, err := m.dirs()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if !validSessionID(id) {
		result.Error = "invalid session ID"
		return result
	}
	lease, err := sessionlock.Acquire(sessionsDir, id)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer lease.Release()
	store, err := localfile.New(sessionsDir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	info, err := store.Inspect(ctx, session.ID(id))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	meta := Session{ID: id, CreatedAt: info.Session.CreatedAt}
	if fileInfo, statErr := os.Stat(filepath.Join(sessionsDir, id+".session.jsonl")); statErr == nil {
		meta.UpdatedAt = fileInfo.ModTime().UTC()
		if details, metadataErr := readMetadata(ctx, store, sessionsDir, sessionstore.SessionInfo{ID: session.ID(id), LastUpdatedAt: meta.UpdatedAt}); metadataErr == nil {
			meta = details
		}
	}
	paths, err := m.artifacts(ctx, store, sessionsDir, id)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(paths) == 0 {
		result.Error = "session has no managed files to archive"
		return result
	}
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		result.Error = fmt.Errorf("create session trash: %w", err).Error()
		return result
	}
	if err := checkDirectoryIfExists(trashDir); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.Chmod(trashDir, 0o700); err != nil {
		result.Error = fmt.Errorf("secure session trash: %w", err).Error()
		return result
	}
	trashID, err := newTrashID()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	entryDir := filepath.Join(trashDir, trashID)
	if err := os.Mkdir(entryDir, 0o700); err != nil {
		result.Error = fmt.Errorf("create archive entry: %w", err).Error()
		return result
	}
	manifest := archiveManifest{Version: archiveVersion, TrashID: trashID, SessionID: id, ArchivedAt: time.Now().UTC(), Workspace: meta.Workspace, Title: meta.Title, State: "archiving", Files: make([]string, 0, len(paths))}
	for _, path := range paths {
		rel, err := filepath.Rel(sessionsDir, path)
		if err != nil || !safeRelative(rel) {
			_ = os.RemoveAll(entryDir)
			result.Error = "managed artifact path escaped session directory"
			return result
		}
		manifest.Files = append(manifest.Files, rel)
	}
	if err := writeManifest(entryDir, manifest); err != nil {
		_ = os.RemoveAll(entryDir)
		result.Error = fmt.Errorf("write archive manifest: %w", err).Error()
		return result
	}
	moved := make([]string, 0, len(paths))
	for i, source := range paths {
		if err := ctx.Err(); err != nil {
			result.Error = err.Error()
			break
		}
		info, err := os.Lstat(source)
		if err != nil {
			result.Error = fmt.Errorf("inspect managed artifact: %w", err).Error()
			break
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			result.Error = fmt.Sprintf("refusing unsupported managed artifact %q", source)
			break
		}
		if info.IsDir() {
			if err := validateTree(source); err != nil {
				result.Error = fmt.Errorf("refusing unsafe managed artifact tree %q: %w", source, err).Error()
				break
			}
		}
		dest := filepath.Join(entryDir, "payload", manifest.Files[i])
		if err := secureMkdirAll(entryDir, filepath.Dir(dest)); err != nil {
			result.Error = fmt.Errorf("create archive payload: %w", err).Error()
			break
		}
		if err := os.Rename(source, dest); err != nil {
			result.Error = fmt.Errorf("archive %s: %w", manifest.Files[i], err).Error()
			break
		}
		moved = append(moved, source)
	}
	if len(moved) != len(paths) {
		rollbackErr := rollbackMoves(entryDir, sessionsDir, manifest.Files, moved)
		if rollbackErr != nil {
			result.Error += "; rollback incomplete: " + rollbackErr.Error() + "; recover files from trash entry " + trashID
			result.TrashID = trashID
			return result
		}
		_ = os.RemoveAll(entryDir)
		return result
	}
	manifest.State = "archived"
	if err := writeManifest(entryDir, manifest); err != nil {
		rollbackErr := rollbackMoves(entryDir, sessionsDir, manifest.Files, moved)
		if rollbackErr != nil {
			result.Error = fmt.Sprintf("publish archive state: %v; rollback incomplete: %v; recover files from trash entry %s", err, rollbackErr, trashID)
			result.TrashID = trashID
			return result
		}
		_ = os.RemoveAll(entryDir)
		result.Error = fmt.Errorf("publish archive state: %w", err).Error()
		return result
	}
	result.OK, result.TrashID = true, trashID
	invalidateMetadataCache(sessionsDir, id)
	return result
}

func (m Manager) artifacts(ctx context.Context, store *localfile.Store, sessionsDir, id string) ([]string, error) {
	paths := []string{filepath.Join(sessionsDir, id+".session.jsonl")}
	paths = append(paths, presentation.SessionDir(sessionsDir, id))
	paths = append(paths, attachments.SessionPDFPageDir(sessionsDir, id))
	paths = append(paths, attachments.SessionImageDir(sessionsDir, id))
	operationDir := filepath.Join(sessionsDir, "operations")
	paths = append(paths, contextSnapshotPath(sessionsDir, id))
	paths = append(paths, contextUsagePath(sessionsDir, id))
	paths = append(paths, contextCheckpointPath(sessionsDir, id))
	paths = append(paths, contextCompactionUsagePath(sessionsDir, id))
	paths = append(paths, outputCompactionPath(operationDir, id))
	resume, err := store.Resume(ctx, session.ID(id))
	if err != nil {
		return nil, err
	}
	addOperationPaths(&paths, operationDir, resume.Operations)
	page, err := store.Items(ctx, session.ID(id), 0, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	for _, item := range page.Items {
		if item.Kind != sessionstore.ItemToolCallStatus {
			continue
		}
		status, ok := item.Data.(sessionstore.ToolCallStatus)
		if !ok {
			continue
		}
		addOperationPaths(&paths, operationDir, status.Operations)
	}
	seen := make(map[string]bool)
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			return nil, fmt.Errorf("refusing symlink or unsupported session artifact %q", path)
		}
		result = append(result, path)
	}
	return result, nil
}

func addOperationPaths(paths *[]string, operationDir string, operations []operation.Operation) {
	for _, op := range operations {
		if !safeComponent(string(op.ID)) || op.Type != operation.TypeShell {
			continue
		}
		state, decodeErr := operation.DecodeShellState(op)
		if decodeErr != nil || filepath.Clean(state.BaseDirectory) != filepath.Clean(operationDir) {
			continue
		}
		candidate := filepath.Join(operationDir, string(op.ID))
		if within(operationDir, candidate) {
			*paths = append(*paths, candidate)
		}
	}
}

func (m Manager) ListTrash(ctx context.Context, query string) ([]TrashEntry, error) {
	_, trashDir, err := m.dirs()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(trashDir)
	if errors.Is(err, fs.ErrNotExist) {
		return []TrashEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]TrashEntry, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !validTrashID(entry.Name()) {
			continue
		}
		manifest, err := readManifest(filepath.Join(trashDir, entry.Name()))
		if err != nil {
			continue
		}
		item := TrashEntry{TrashID: manifest.TrashID, SessionID: manifest.SessionID, ArchivedAt: manifest.ArchivedAt, Workspace: manifest.Workspace, Title: manifest.Title, State: manifest.State}
		if query == "" || strings.Contains(strings.ToLower(item.SessionID+" "+item.Workspace+" "+item.Title), query) {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ArchivedAt.After(result[j].ArchivedAt) })
	return result, nil
}

// Restore moves one or more trash entries back to the live session store.
func (m Manager) Restore(ctx context.Context, trashIDs []string, activeIDs map[string]bool) []Result {
	results := make([]Result, 0, len(trashIDs))
	seen := make(map[string]bool, len(trashIDs))
	for _, trashID := range trashIDs {
		result := Result{TrashID: trashID}
		if ctx.Err() != nil {
			result.Error = ctx.Err().Error()
			results = append(results, result)
			continue
		}
		if seen[trashID] {
			result.Error = "duplicate trash ID in request"
			results = append(results, result)
			continue
		}
		seen[trashID] = true
		result = m.restoreOne(ctx, trashID, activeIDs)
		results = append(results, result)
	}
	return results
}

// Purge permanently removes an already archived session and only the
// harness-managed files stored in its validated trash entry.
func (m Manager) Purge(ctx context.Context, trashIDs []string, activeIDs map[string]bool) []Result {
	results := make([]Result, 0, len(trashIDs))
	seen := make(map[string]bool, len(trashIDs))
	for _, trashID := range trashIDs {
		result := Result{TrashID: trashID}
		if err := ctx.Err(); err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		if seen[trashID] {
			result.Error = "duplicate trash ID in request"
			results = append(results, result)
			continue
		}
		seen[trashID] = true
		results = append(results, m.purgeOne(ctx, trashID, activeIDs))
	}
	return results
}

func (m Manager) purgeOne(ctx context.Context, trashID string, activeIDs map[string]bool) Result {
	result := Result{TrashID: trashID}
	if !validTrashID(trashID) {
		result.Error = "invalid trash ID"
		return result
	}
	sessionsDir, trashDir, err := m.dirs()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	entryDir := filepath.Join(trashDir, trashID)
	manifest, err := readManifest(entryDir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.SessionID = manifest.SessionID
	if manifest.State != "archived" {
		result.Error = "cannot permanently delete an incomplete archive; restore it to recover the session first"
		return result
	}
	if activeIDs[manifest.SessionID] {
		result.Error = sessionlock.ErrBusy.Error()
		return result
	}
	lease, err := sessionlock.Acquire(sessionsDir, manifest.SessionID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer lease.Release()
	payload := filepath.Join(entryDir, "payload")
	if _, err := os.Lstat(payload); err != nil {
		result.Error = fmt.Errorf("inspect archived payload: %w", err).Error()
		return result
	}
	if err := validateTree(entryDir); err != nil {
		result.Error = fmt.Errorf("refusing unsafe trash entry: %w", err).Error()
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Error = err.Error()
		return result
	}
	if err := os.RemoveAll(entryDir); err != nil {
		result.Error = fmt.Errorf("permanently remove archived session: %w", err).Error()
		return result
	}
	invalidateMetadataCache(sessionsDir, manifest.SessionID)
	result.OK = true
	return result
}

func (m Manager) restoreOne(ctx context.Context, trashID string, activeIDs map[string]bool) Result {
	result := Result{TrashID: trashID}
	if !validTrashID(trashID) {
		result.Error = "invalid trash ID"
		return result
	}
	sessionsDir, trashDir, err := m.dirs()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	entryDir := filepath.Join(trashDir, trashID)
	manifest, err := readManifest(entryDir)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.SessionID = manifest.SessionID
	if activeIDs[manifest.SessionID] {
		result.Error = sessionlock.ErrBusy.Error()
		return result
	}
	lease, err := sessionlock.Acquire(sessionsDir, manifest.SessionID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer lease.Release()
	if manifest.State == "archiving" {
		if err := rollbackIncompleteArchive(entryDir, sessionsDir, manifest); err != nil {
			result.Error = fmt.Errorf("recover interrupted archive: %w", err).Error()
			return result
		}
		result.OK = true
		return result
	}
	for _, rel := range manifest.Files {
		if !validManagedPath(manifest.SessionID, rel) {
			result.Error = "archive manifest contains an unsafe path"
			return result
		}
		dest := filepath.Join(sessionsDir, rel)
		source := filepath.Join(entryDir, "payload", rel)
		sourceInfo, sourceErr := os.Lstat(source)
		destInfo, destErr := os.Lstat(dest)
		sourceExists, destExists := sourceErr == nil, destErr == nil
		if sourceErr != nil && !errors.Is(sourceErr, fs.ErrNotExist) {
			result.Error = sourceErr.Error()
			return result
		}
		if destErr != nil && !errors.Is(destErr, fs.ErrNotExist) {
			result.Error = destErr.Error()
			return result
		}
		if sourceExists && destExists {
			result.Error = fmt.Sprintf("both archived and live copies exist for %q", rel)
			return result
		}
		if !sourceExists && !destExists {
			result.Error = fmt.Sprintf("artifact %q is missing from both archive and session store", rel)
			return result
		}
		if sourceExists && (sourceInfo.Mode()&os.ModeSymlink != 0 || (!sourceInfo.Mode().IsRegular() && !sourceInfo.IsDir())) {
			result.Error = fmt.Sprintf("unsafe archived artifact %q", rel)
			return result
		}
		if destExists && (destInfo.Mode()&os.ModeSymlink != 0 || (!destInfo.Mode().IsRegular() && !destInfo.IsDir())) {
			result.Error = fmt.Sprintf("unsafe live artifact %q", rel)
			return result
		}
	}
	moved := make([]string, 0, len(manifest.Files))
	restoredCount := 0
	for _, rel := range manifest.Files {
		if err := ctx.Err(); err != nil {
			result.Error = err.Error()
			break
		}
		source := filepath.Join(entryDir, "payload", rel)
		dest := filepath.Join(sessionsDir, rel)
		if _, err := os.Lstat(source); errors.Is(err, fs.ErrNotExist) {
			if _, destErr := os.Lstat(dest); destErr == nil {
				restoredCount++
				continue
			}
		}
		if info, err := os.Lstat(source); err == nil && info.IsDir() {
			if err := validateTree(source); err != nil {
				result.Error = fmt.Errorf("unsafe archived tree %q: %w", rel, err).Error()
				break
			}
		}
		if err := secureMkdirAll(sessionsDir, filepath.Dir(dest)); err != nil {
			result.Error = err.Error()
			break
		}
		if err := os.Rename(source, dest); err != nil {
			result.Error = fmt.Errorf("restore %s: %w", rel, err).Error()
			break
		}
		moved = append(moved, rel)
		restoredCount++
	}
	if restoredCount != len(manifest.Files) {
		rollbackErr := rollbackRestore(entryDir, sessionsDir, moved)
		if rollbackErr != nil {
			result.Error += "; rollback incomplete: " + rollbackErr.Error()
		}
		return result
	}
	if err := os.RemoveAll(entryDir); err != nil {
		result.OK = true
		result.Error = fmt.Errorf("restored session, but archive metadata remains in trash: %w", err).Error()
		return result
	}
	invalidateMetadataCache(sessionsDir, manifest.SessionID)
	result.OK = true
	return result
}

func validSessionID(id string) bool {
	if id == "" {
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

func safeComponent(value string) bool {
	return value != "" && filepath.Base(value) == value && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

func safeRelative(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return false
	}
	return !strings.ContainsRune(value, 0)
}

func validManagedPath(sessionID, rel string) bool {
	if !safeRelative(rel) {
		return false
	}
	if rel == sessionID+".session.jsonl" {
		return true
	}
	sum := sha256.Sum256([]byte(sessionID))
	digest := hex.EncodeToString(sum[:])
	if rel == digest+".context.json" || rel == digest+".context-usage.json" || rel == digest+".context-checkpoint.json" || rel == digest+".context-compaction-usage.json" || rel == filepath.Join("operations", "output-compaction", digest) || rel == filepath.Join("presentation", digest) {
		return true
	}
	if rel == filepath.Join("pdf-pages", digest) || rel == filepath.Join("inline-images", digest) {
		return true
	}
	if filepath.Dir(rel) == "operations" && safeComponent(filepath.Base(rel)) && validSessionID(filepath.Base(rel)) {
		return true
	}
	return false
}

func secureMkdirAll(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil || !within(root, target) {
		return errors.New("directory escaped trusted root")
	}
	rel, _ := filepath.Rel(root, target)
	current := root
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("refusing unsafe directory root %q", root)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing unsafe directory component %q", current)
		}
	}
	return nil
}

func writeManifest(dir string, value archiveManifest) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "manifest.json")
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readManifest(dir string) (archiveManifest, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return archiveManifest{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return archiveManifest{}, errors.New("trash entry is not a regular directory")
	}
	path := filepath.Join(dir, "manifest.json")
	manifestInfo, err := os.Lstat(path)
	if err != nil {
		return archiveManifest{}, err
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 || manifestInfo.Size() > 1<<20 {
		return archiveManifest{}, errors.New("trash manifest is not a safe regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return archiveManifest{}, err
	}
	var manifest archiveManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return archiveManifest{}, err
	}
	if manifest.Version != archiveVersion || !validTrashID(manifest.TrashID) || filepath.Base(dir) != manifest.TrashID || !validSessionID(manifest.SessionID) || manifest.ArchivedAt.IsZero() || len(manifest.Files) == 0 || (manifest.State != "archiving" && manifest.State != "archived") {
		return archiveManifest{}, errors.New("invalid session trash manifest")
	}
	seen := make(map[string]bool, len(manifest.Files))
	for _, path := range manifest.Files {
		if !validManagedPath(manifest.SessionID, path) || seen[path] {
			return archiveManifest{}, errors.New("trash manifest contains unsafe or duplicate paths")
		}
		seen[path] = true
	}
	return manifest, nil
}

func rollbackMoves(entryDir, sessionsDir string, files, moved []string) error {
	var errs []error
	for i := len(moved) - 1; i >= 0; i-- {
		rel := files[i]
		source := filepath.Join(entryDir, "payload", rel)
		dest := filepath.Join(sessionsDir, rel)
		if err := secureMkdirAll(sessionsDir, filepath.Dir(dest)); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(source, dest); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func rollbackRestore(entryDir, sessionsDir string, moved []string) error {
	var errs []error
	for i := len(moved) - 1; i >= 0; i-- {
		rel := moved[i]
		source := filepath.Join(sessionsDir, rel)
		dest := filepath.Join(entryDir, "payload", rel)
		if err := secureMkdirAll(entryDir, filepath.Dir(dest)); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(source, dest); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func rollbackIncompleteArchive(entryDir, sessionsDir string, manifest archiveManifest) error {
	var errs []error
	for i := len(manifest.Files) - 1; i >= 0; i-- {
		rel := manifest.Files[i]
		if !validManagedPath(manifest.SessionID, rel) {
			errs = append(errs, errors.New("archive manifest contains an unsafe path"))
			continue
		}
		source := filepath.Join(entryDir, "payload", rel)
		dest := filepath.Join(sessionsDir, rel)
		_, sourceErr := os.Lstat(source)
		_, destErr := os.Lstat(dest)
		sourceExists, destExists := sourceErr == nil, destErr == nil
		if sourceErr != nil && !errors.Is(sourceErr, fs.ErrNotExist) {
			errs = append(errs, sourceErr)
			continue
		}
		if destErr != nil && !errors.Is(destErr, fs.ErrNotExist) {
			errs = append(errs, destErr)
			continue
		}
		if sourceExists && destExists {
			errs = append(errs, fmt.Errorf("both copies exist for %s", rel))
			continue
		}
		if !sourceExists && !destExists {
			errs = append(errs, fmt.Errorf("both copies are missing for %s", rel))
			continue
		}
		if !sourceExists {
			continue
		}
		if err := secureMkdirAll(sessionsDir, filepath.Dir(dest)); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(source, dest); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return os.RemoveAll(entryDir)
}

func validateTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("contains symlink or unsupported file %q", path)
		}
		return nil
	})
}

func (m Manager) dirs() (string, string, error) {
	if strings.TrimSpace(m.SessionDir) == "" {
		return "", "", errors.New("session directory is required")
	}
	sessions, err := filepath.Abs(m.SessionDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve session directory: %w", err)
	}
	trash := m.TrashDir
	if strings.TrimSpace(trash) == "" {
		trash = filepath.Join(filepath.Dir(sessions), "trash", "sessions")
	}
	trash, err = filepath.Abs(trash)
	if err != nil {
		return "", "", fmt.Errorf("resolve session trash: %w", err)
	}
	if within(sessions, trash) || within(trash, sessions) {
		return "", "", errors.New("session and trash directories must not contain one another")
	}
	if err := checkDirectoryIfExists(sessions); err != nil {
		return "", "", err
	}
	if err := checkDirectoryIfExists(trash); err != nil {
		return "", "", err
	}
	return sessions, trash, nil
}

func checkDirectoryIfExists(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing symlink or non-directory storage root %q", path)
	}
	return nil
}

func (m Manager) List(ctx context.Context, opts ListOptions) ([]Session, error) {
	sessionsDir, _, err := m.dirs()
	if err != nil {
		return nil, err
	}
	cacheDir := canonicalMetadataDir(sessionsDir)
	store, err := localfile.New(cacheDir)
	if err != nil {
		return nil, err
	}
	infos, err := store.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(opts.Query))
	workspaceFilter := filepath.Clean(strings.TrimSpace(opts.Workspace))
	result := make([]Session, 0, len(infos))
	for _, info := range infos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		logInfo, logErr := os.Lstat(filepath.Join(cacheDir, string(info.ID)+".session.jsonl"))
		key, files, cacheable := metadataCacheIdentityForLogResolved(cacheDir, string(info.ID), logInfo, info.LastUpdatedAt)
		if logErr != nil {
			cacheable = false
		}
		meta, hit := Session{}, false
		if cacheable {
			meta, hit = cachedMetadata(key, files)
		}
		if !hit {
			meta, err = readMetadata(ctx, store, cacheDir, info)
			if err != nil {
				// An unreadable or corrupt session should remain addressable for recovery.
				meta = Session{ID: string(info.ID), UpdatedAt: info.LastUpdatedAt}
			} else if cacheable && ctx.Err() == nil {
				cacheMetadataIfUnchanged(key, files, meta)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		meta.Active = opts.ActiveIDs[meta.ID]
		if !meta.Active {
			busy, lockErr := sessionlock.IsBusy(cacheDir, meta.ID)
			if lockErr != nil {
				return nil, fmt.Errorf("inspect active session %s: %w", meta.ID, lockErr)
			}
			meta.Active = busy
		}
		if workspaceFilter != "." && workspaceFilter != "" && filepath.Clean(meta.Workspace) != workspaceFilter {
			continue
		}
		if query != "" && !matches(meta, query) {
			continue
		}
		result = append(result, meta)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

// Get returns metadata for one ID without scanning unrelated sessions.
func (m Manager) Get(ctx context.Context, id string, activeIDs map[string]bool) (Session, error) {
	if !validSessionID(id) {
		return Session{}, errors.New("invalid session ID")
	}
	sessionsDir, _, err := m.dirs()
	if err != nil {
		return Session{}, err
	}
	cacheDir := canonicalMetadataDir(sessionsDir)
	store, err := localfile.New(cacheDir)
	if err != nil {
		return Session{}, err
	}
	// Capture the log and sidecar identities before validating/reading session
	// data. The same baseline is compared after Items so a replacement during
	// Inspect or Items cannot seed the cache with a mixed summary.
	logPath := filepath.Join(cacheDir, id+".session.jsonl")
	logInfo, statErr := os.Lstat(logPath)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return Session{}, statErr
	}
	var cacheKey metadataCacheKey
	var cacheFiles metadataFiles
	cacheable := false
	if statErr == nil {
		cacheKey, cacheFiles, cacheable = metadataCacheIdentityForLogResolved(cacheDir, id, logInfo, logInfo.ModTime().UTC())
		if cacheable {
			if meta, hit := cachedMetadata(cacheKey, cacheFiles); hit {
				if err := ctx.Err(); err != nil {
					return Session{}, err
				}
				meta.Active = activeIDs[id]
				if !meta.Active {
					meta.Active, err = sessionlock.IsBusy(cacheDir, id)
				}
				return meta, err
			}
		}
	}
	info, err := store.Inspect(ctx, session.ID(id))
	if err != nil {
		return Session{}, err
	}
	if statErr != nil {
		// Preserve the store's validation/error for missing records, but guard
		// the uncommon case where the file appeared between Lstat and Inspect.
		logInfo, err = os.Lstat(logPath)
		if err != nil {
			return Session{}, err
		}
	}
	infoForMetadata := sessionstore.SessionInfo{ID: session.ID(id), LastUpdatedAt: logInfo.ModTime().UTC()}
	meta, err := readMetadataFromSnapshot(ctx, store, cacheDir, infoForMetadata, info)
	if err != nil {
		return Session{}, err
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = info.Session.CreatedAt
	}
	meta.Active = activeIDs[id]
	if !meta.Active {
		meta.Active, err = sessionlock.IsBusy(cacheDir, id)
	}
	if err == nil && cacheable && ctx.Err() == nil {
		cacheMetadataIfUnchanged(cacheKey, cacheFiles, meta)
	}
	return meta, err
}

func readMetadata(ctx context.Context, store *localfile.Store, sessionsDir string, info sessionstore.SessionInfo) (Session, error) {
	snapshot, err := store.Inspect(ctx, info.ID)
	if err != nil {
		return Session{}, err
	}
	return readMetadataFromSnapshot(ctx, store, sessionsDir, info, snapshot)
}

func readMetadataFromSnapshot(ctx context.Context, store *localfile.Store, sessionsDir string, info sessionstore.SessionInfo, snapshot sessionstore.Snapshot) (Session, error) {
	meta := Session{ID: string(info.ID), CreatedAt: snapshot.Session.CreatedAt, UpdatedAt: info.LastUpdatedAt}
	if data, err := os.ReadFile(contextSnapshotPath(sessionsDir, string(info.ID))); err == nil {
		var context contextMetadata
		if json.Unmarshal(data, &context) == nil {
			meta.Workspace = context.Workspace
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Session{}, err
	}
	page, err := store.Items(ctx, info.ID, 0, maxMetadataItems)
	if err != nil {
		return Session{}, err
	}
	meta.ItemCount = len(page.Items)
	for _, item := range page.Items {
		if item.Kind == sessionstore.ItemInput {
			input, ok := item.Data.(inbox.Input)
			if !ok || input.Kind != inbox.InputExternal || meta.Title != "" {
				continue
			}
			var prompt string
			if json.Unmarshal([]byte(input.Payload), &prompt) == nil {
				meta.Title = compact(prompt, 96)
			}
		}
		if item.Kind != sessionstore.ItemModelResponse {
			continue
		}
		response, ok := item.Data.(sessionstore.ModelResponse)
		if !ok {
			continue
		}
		var parts []string
		for _, output := range response.Response.Output {
			if output.Type != llm.ItemMessage {
				continue
			}
			message, ok := output.Data.(llm.Message)
			if ok && message.Role == llm.RoleAssistant && message.Phase != "analysis" && strings.TrimSpace(message.Text) != "" {
				parts = append(parts, message.Text)
			}
		}
		if len(parts) > 0 {
			meta.Preview = compact(strings.Join(parts, "\n"), defaultPreviewRunes)
		}
	}
	return meta, nil
}

func compact(value string, maxRunes int) string {
	// Build only the normalized prefix needed for the display preview. The old
	// strings.Fields + []rune path materialized the entire assistant response,
	// often tens or hundreds of KiB, even though callers retain at most 320 runes.
	runes := make([]rune, 0, maxRunes+1)
	pendingSpace := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			if len(runes) > 0 {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			if len(runes) == maxRunes {
				return string(runes) + "…"
			}
			runes = append(runes, ' ')
			pendingSpace = false
		}
		runes = append(runes, r)
		if len(runes) > maxRunes {
			return string(runes[:maxRunes]) + "…"
		}
	}
	return string(runes)
}

func matches(item Session, query string) bool {
	for _, value := range []string{item.ID, item.Workspace, item.Title, item.Preview} {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func contextSnapshotPath(dir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".context.json")
}

func contextUsagePath(dir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".context-usage.json")
}

func contextCheckpointPath(dir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".context-checkpoint.json")
}

func contextCompactionUsagePath(dir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".context-compaction-usage.json")
}

func outputCompactionPath(operationDir, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(operationDir, "output-compaction", hex.EncodeToString(sum[:]))
}

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func newTrashID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(b[:]), nil
}

func validTrashID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' || r == 'Z' || r == 'T' {
			continue
		}
		return false
	}
	return true
}

var _ = operation.TypeShell
