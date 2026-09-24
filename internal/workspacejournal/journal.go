// Package workspacejournal records successful file-tool mutations in a
// bounded, content-addressed store so they can be inspected and selectively
// restored. It is recovery tooling, not a sandbox: Bash and external edits are
// unobserved, so an observed absence of entries never means the workspace is
// clean.
//
// Layout under a caller-provided root (normally PK_HOME/journal):
//
//	objects/<ab>/<sha256...>        content blobs, mode 0600
//	sessions/<session>/journal.jsonl durable append-only entry log
//	sessions/<session>/restores/    pre-restore snapshots, one directory each
//
// Durability contract: Begin stores both the preimage and the intended
// postimage and fsyncs the "prepared" line before the caller mutates the
// workspace. Complete and Fail append the terminal status afterward. A
// "prepared" entry without a terminal line therefore means completion is
// unknown: the workspace may or may not contain the change. Restoration only
// ever uses the stored preimage and refuses when the current file does not
// match the recorded postimage fingerprint.
package workspacejournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits bounds journal growth. Zero fields select the defaults.
type Limits struct {
	// MaxEntriesPerSession bounds folded entries retained per session journal.
	// Older entries are dropped from the readable fold; their objects remain
	// until object-store garbage collection removes unreferenced blobs.
	MaxEntriesPerSession int
	// MaxObjectBytes bounds the total size of the content-object store. When
	// exceeded, GC removes blobs unreferenced by any scanned journal.
	MaxObjectBytes int64
	// MaxSessionsScanned bounds each GC journal scan; when the scan hits the
	// bound, GC is skipped for that pass rather than deleting possibly
	// referenced objects.
	MaxSessionsScanned int
}

const (
	defaultMaxEntriesPerSession = 256
	defaultMaxObjectBytes       = 256 << 20
	defaultMaxSessionsScanned   = 4096
	objectDirPerm               = 0o700
	filePerm                    = 0o600
)

func (l Limits) withDefaults() Limits {
	if l.MaxEntriesPerSession <= 0 {
		l.MaxEntriesPerSession = defaultMaxEntriesPerSession
	}
	if l.MaxObjectBytes <= 0 {
		l.MaxObjectBytes = defaultMaxObjectBytes
	}
	if l.MaxSessionsScanned <= 0 {
		l.MaxSessionsScanned = defaultMaxSessionsScanned
	}
	return l
}

// Status values for one recorded operation.
const (
	StatusPrepared  = "prepared"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusRestored  = "restored"
)

// Content identifies one stored blob.
type Content struct {
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// Restore records one user-initiated restore of this operation.
type Restore struct {
	Time        string `json:"time"`
	PreSHA256   string `json:"pre_sha256"`   // content restored onto the file
	SnapshotRef string `json:"snapshot_ref"` // pre-restore snapshot directory name
}

// Op is the folded, reader-facing view of one recorded file mutation.
type Op struct {
	Seq      uint64    `json:"seq"`              // sequence of the prepared line
	ID       string    `json:"id"`               // stable operation ID
	CallID   string    `json:"call_id,omitempty"` // model tool-call ID, may be empty
	Action   string    `json:"action"`           // WriteFile or EditFile
	Path     string    `json:"path"`             // workspace-relative slash path
	Status   string    `json:"status"`           // prepared/completed/failed/restored
	Pre      *Content  `json:"pre"`              // nil when the file did not exist
	Post     *Content  `json:"post"`             // nil for failed operations
	Mode     uint32    `json:"mode"`             // recorded file mode of the post state
	Time     time.Time `json:"time"`             // prepared time
	Reason   string    `json:"reason,omitempty"` // failure or restore note
	Restores []Restore `json:"restores,omitempty"`
}

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Store is one journal root. Methods are safe for concurrent use; each session
// directory is guarded by a per-session mutex and file operations use atomic
// replacement for content objects.
type Store struct {
	root   string
	limits Limits

	mu        sync.Mutex
	sessions  map[string]*sessionLock
	gcMu      sync.Mutex
	gcStarted bool
}

type sessionLock struct {
	mu sync.Mutex
}

// Open prepares (creating if needed) the journal root directories.
func Open(root string, limits Limits) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("journal root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve journal root: %w", err)
	}
	for _, dir := range []string{absolute, filepath.Join(absolute, "objects"), filepath.Join(absolute, "sessions")} {
		if err := os.MkdirAll(dir, objectDirPerm); err != nil {
			return nil, fmt.Errorf("create journal directory: %w", err)
		}
	}
	return &Store{root: absolute, limits: limits.withDefaults(), sessions: map[string]*sessionLock{}}, nil
}

// Root returns the absolute journal root path.
func (s *Store) Root() string { return s.root }

func validSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

func (s *Store) sessionDir(session string) string {
	return filepath.Join(s.root, "sessions", session)
}

func (s *Store) lockSession(session string) *sessionLock {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.sessions[session]
	if lock == nil {
		lock = &sessionLock{}
		s.sessions[session] = lock
	}
	return lock
}

func objectPath(root, digest string) (string, error) {
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("invalid object digest %q", digest)
	}
	for _, r := range digest {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return "", fmt.Errorf("invalid object digest %q", digest)
		}
	}
	return filepath.Join(root, "objects", digest[:2], digest), nil
}

func (s *Store) writeObject(data []byte) (Content, error) {
	digest := sha256.Sum256(data)
	hexDigest := hex.EncodeToString(digest[:])
	path, err := objectPath(s.root, hexDigest)
	if err != nil {
		return Content{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), objectDirPerm); err != nil {
		return Content{}, fmt.Errorf("create object directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return Content{SHA256: hexDigest, Size: len(data)}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Content{}, fmt.Errorf("inspect object: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".obj-*")
	if err != nil {
		return Content{}, fmt.Errorf("create object: %w", err)
	}
	name := temp.Name()
	writeErr := func() error {
		if err := temp.Chmod(filePerm); err != nil {
			return err
		}
		if _, err := temp.Write(data); err != nil {
			return err
		}
		return temp.Sync()
	}()
	closeErr := temp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = os.Rename(name, path)
	}
	if writeErr != nil {
		_ = os.Remove(name)
		return Content{}, fmt.Errorf("write object: %w", writeErr)
	}
	_ = syncDir(filepath.Dir(path))
	return Content{SHA256: hexDigest, Size: len(data)}, nil
}

// ReadObject returns the stored content of one journal object reference. It
// is used by bounded diff rendering and by restore; content is verified
// against the recorded digest before it is returned.
func (s *Store) ReadObject(content Content) ([]byte, error) {
	return s.readObject(content)
}

func (s *Store) readObject(content Content) ([]byte, error) {
	if content == (Content{}) || content.SHA256 == "" {
		return nil, errors.New("journal object reference is missing")
	}
	path, err := objectPath(s.root, content.SHA256)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("journal object %s is missing", content.SHA256[:12])
		}
		return nil, fmt.Errorf("read journal object: %w", err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != content.SHA256 {
		return nil, fmt.Errorf("journal object %s failed its digest check", content.SHA256[:12])
	}
	if content.Size != 0 && len(data) != content.Size {
		return nil, fmt.Errorf("journal object %s has unexpected size", content.SHA256[:12])
	}
	return data, nil
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// preparedLine is one line of the append-only journal.
type preparedLine struct {
	Kind    string       `json:"kind"` // prepare/complete/fail/restore
	Seq     uint64       `json:"seq"`
	ID      string       `json:"id"`
	CallID  string       `json:"call_id,omitempty"`
	Action  string       `json:"action,omitempty"`
	Path    string       `json:"path,omitempty"`
	Pre     *Content     `json:"pre,omitempty"`
	Post    *Content     `json:"post,omitempty"`
	Mode    uint32       `json:"mode,omitempty"`
	Reason  string       `json:"reason,omitempty"`
	Restore *restoreNote `json:"restore,omitempty"`
	Time    time.Time    `json:"time"`
}

type restoreNote struct {
	PreSHA256   string `json:"pre_sha256,omitempty"`
	SnapshotRef string `json:"snapshot_ref,omitempty"`
}

// Begin durably records the prepared state of one mutation. Call it before
// mutating the workspace; if Begin fails, do not mutate.
func (s *Store) Begin(session, opID, callID, action, path string, pre, post []byte, mode os.FileMode) error {
	if !validSessionID(session) {
		return errors.New("journal session ID must be 32 hexadecimal characters")
	}
	if strings.TrimSpace(opID) == "" || len(opID) > 128 {
		return errors.New("journal operation ID is required")
	}
	rel, err := cleanJournalPath(path)
	if err != nil {
		return err
	}
	lock := s.lockSession(session)
	lock.mu.Lock()
	defer lock.mu.Unlock()

	var preContent *Content
	if pre != nil {
		stored, err := s.writeObject(pre)
		if err != nil {
			return fmt.Errorf("store preimage: %w", err)
		}
		preContent = &stored
	}
	postContent, err := s.writeObject(post)
	if err != nil {
		return fmt.Errorf("store postimage: %w", err)
	}
	seq, err := s.nextSeqLocked(session)
	if err != nil {
		return err
	}
	line := preparedLine{
		Kind: "prepare", Seq: seq, ID: opID, CallID: callID, Action: action, Path: rel,
		Pre: preContent, Post: &postContent, Mode: uint32(mode.Perm()), Time: time.Now().UTC(),
	}
	if err := s.appendLine(session, line); err != nil {
		return err
	}
	return nil
}

// Complete records that the prepared mutation committed to the workspace.
func (s *Store) Complete(session, opID string) error {
	return s.appendStatus(session, opID, "complete", "")
}

// Fail records that the prepared mutation did not commit to the workspace.
func (s *Store) Fail(session, opID, reason string) error {
	return s.appendStatus(session, opID, "fail", reason)
}

func (s *Store) appendStatus(session, opID, kind, reason string) error {
	if !validSessionID(session) {
		return errors.New("journal session ID must be 32 hexadecimal characters")
	}
	lock := s.lockSession(session)
	lock.mu.Lock()
	defer lock.mu.Unlock()
	seq, err := s.nextSeqLocked(session)
	if err != nil {
		return err
	}
	return s.appendLine(session, preparedLine{Kind: kind, Seq: seq, ID: opID, Reason: reason, Time: time.Now().UTC()})
}

// nextSeqLocked derives the next sequence number from the journal itself so
// sequence allocation survives crashes and torn writes. The caller must hold
// the session lock.
func (s *Store) nextSeqLocked(session string) (uint64, error) {
	lines, _, err := readRawLines(s.journalPath(session))
	if err != nil {
		return 0, err
	}
	var last uint64
	for _, line := range lines {
		if line.Seq > last {
			last = line.Seq
		}
	}
	return last + 1, nil
}

func (s *Store) journalPath(session string) string {
	return filepath.Join(s.sessionDir(session), "journal.jsonl")
}

func (s *Store) appendLine(session string, line preparedLine) error {
	dir := s.sessionDir(session)
	if err := os.MkdirAll(dir, objectDirPerm); err != nil {
		return fmt.Errorf("create session journal directory: %w", err)
	}
	encoded, err := marshalLine(line)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.journalPath(session), os.O_WRONLY|os.O_CREATE|os.O_APPEND, filePerm)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("append journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync journal: %w", err)
	}
	return nil
}

// cleanJournalPath normalizes and validates a workspace-relative journal path.
// Paths are stored with forward slashes and may never escape the workspace.
func cleanJournalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("journal path is required")
	}
	slash := filepath.ToSlash(path)
	if strings.ContainsRune(slash, '\x00') {
		return "", errors.New("journal path contains NUL")
	}
	slash = strings.TrimSuffix(slash, "/")
	if slash == "" || slash == "." || slash == ".." || strings.HasPrefix(slash, "/") {
		return "", fmt.Errorf("journal path %q is not workspace-relative", path)
	}
	for _, part := range strings.Split(slash, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("journal path %q contains an invalid component", path)
		}
	}
	if len(slash) > 4096 {
		return "", errors.New("journal path exceeds 4096 bytes")
	}
	return slash, nil
}

// fold reads the durable journal lines and produces the retained reader view.
// A malformed trailing line (torn write) is ignored; a malformed line followed
// by valid data indicates real corruption and is an error.
func (s *Store) fold(session string) ([]Op, uint64, error) {
	data, err := os.ReadFile(s.journalPath(session))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("read journal: %w", err)
	}
	var lines []preparedLine
	tornTail := false
	offset := 0
	for offset < len(data) {
		end := bytes.IndexByte(data[offset:], '\n')
		if end < 0 {
			tornTail = true
			break
		}
		line := data[offset : offset+end]
		offset += end + 1
		var decoded preparedLine
		if err := unmarshalLine(line, &decoded); err != nil {
			return nil, 0, fmt.Errorf("journal line %d is corrupt", len(lines)+1)
		}
		lines = append(lines, decoded)
	}
	if tornTail && len(lines) == 0 {
		return nil, 0, nil
	}
	return foldLines(lines, s.limits.MaxEntriesPerSession)
}

func foldLines(lines []preparedLine, maxEntries int) ([]Op, uint64, error) {
	var ops []*Op
	byID := map[string]*Op{}
	var lastSeq uint64
	for _, line := range lines {
		if line.Seq > lastSeq {
			lastSeq = line.Seq
		}
		switch line.Kind {
		case "prepare":
			if _, exists := byID[line.ID]; exists {
				continue
			}
			op := &Op{Seq: line.Seq, ID: line.ID, CallID: line.CallID, Action: line.Action, Path: line.Path, Status: StatusPrepared, Pre: line.Pre, Post: line.Post, Mode: line.Mode, Time: line.Time}
			byID[line.ID] = op
			ops = append(ops, op)
		case "complete", "fail":
			op, exists := byID[line.ID]
			if !exists || op.Status != StatusPrepared {
				continue
			}
			if line.Kind == "complete" {
				op.Status = StatusCompleted
			} else {
				op.Status = StatusFailed
				op.Post = nil
				op.Reason = line.Reason
			}
		case "restore":
			op, exists := byID[line.ID]
			if !exists || op.Status == StatusFailed {
				continue
			}
			op.Status = StatusRestored
			if line.Restore != nil {
				op.Restores = append(op.Restores, Restore{Time: line.Time.Format(time.RFC3339Nano), PreSHA256: line.Restore.PreSHA256, SnapshotRef: line.Restore.SnapshotRef})
			}
			if line.Reason != "" {
				op.Reason = line.Reason
			}
		}
	}
	if len(ops) > maxEntries {
		ops = append([]*Op(nil), ops[len(ops)-maxEntries:]...)
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Seq < ops[j].Seq })
	out := make([]Op, 0, len(ops))
	for _, op := range ops {
		out = append(out, *op)
	}
	return out, lastSeq, nil
}

// Cursor is an opaque position in a session journal: the decimal sequence
// number of the last operation the caller has already seen.
func (s *Store) NewestCursor(session string) (string, error) {
	_, lastSeq, err := s.fold(session)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(lastSeq, 10), nil
}
