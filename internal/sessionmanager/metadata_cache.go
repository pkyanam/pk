package sessionmanager

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxMetadataCacheEntries = 256
const maxMetadataCacheStringBytes = 8 << 10

type metadataCacheKey struct {
	dir string
	id  string
}

type metadataFiles struct {
	log        os.FileInfo
	context    os.FileInfo
	hasContext bool
	updatedAt  time.Time
}

type metadataCacheEntry struct {
	value   Session
	files   metadataFiles
	lastUse uint64
}

var listMetadataCache = struct {
	sync.Mutex
	entries map[metadataCacheKey]metadataCacheEntry
	clock   uint64
}{entries: make(map[metadataCacheKey]metadataCacheEntry)}

func metadataCacheIdentity(sessionsDir, id string, updatedAt time.Time) (metadataCacheKey, metadataFiles, bool) {
	dir := filepath.Clean(sessionsDir)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = filepath.Clean(resolved)
	}
	logPath := filepath.Join(dir, id+".session.jsonl")
	logInfo, err := os.Lstat(logPath)
	if err != nil || logInfo.Mode()&os.ModeSymlink != 0 || !logInfo.Mode().IsRegular() || !logInfo.ModTime().UTC().Equal(updatedAt.UTC()) {
		return metadataCacheKey{}, metadataFiles{}, false
	}
	files := metadataFiles{log: logInfo, updatedAt: updatedAt.UTC()}
	contextInfo, err := os.Lstat(contextSnapshotPath(dir, id))
	if errors.Is(err, fs.ErrNotExist) {
		return metadataCacheKey{dir: dir, id: id}, files, true
	}
	if err != nil || contextInfo.Mode()&os.ModeSymlink != 0 || !contextInfo.Mode().IsRegular() {
		return metadataCacheKey{}, metadataFiles{}, false
	}
	files.context = contextInfo
	files.hasContext = true
	return metadataCacheKey{dir: dir, id: id}, files, true
}

func sameMetadataFiles(a, b metadataFiles) bool {
	if !a.updatedAt.Equal(b.updatedAt) || !sameFileSnapshot(a.log, b.log) || a.hasContext != b.hasContext {
		return false
	}
	return !a.hasContext || sameFileSnapshot(a.context, b.context)
}

func sameFileSnapshot(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

func cachedMetadata(key metadataCacheKey, files metadataFiles) (Session, bool) {
	listMetadataCache.Lock()
	defer listMetadataCache.Unlock()
	entry, ok := listMetadataCache.entries[key]
	if !ok || !sameMetadataFiles(entry.files, files) {
		if ok {
			delete(listMetadataCache.entries, key)
		}
		return Session{}, false
	}
	listMetadataCache.clock++
	entry.lastUse = listMetadataCache.clock
	listMetadataCache.entries[key] = entry
	return entry.value, true
}

func cacheMetadata(key metadataCacheKey, files metadataFiles, value Session) {
	// Workspace is copied from a user-managed sidecar. Keep the cache bounded in
	// both entry count and aggregate string size; oversized metadata is still
	// returned to this caller but not retained in process memory.
	if len(value.ID)+len(value.Title)+len(value.Preview)+len(value.Workspace) > maxMetadataCacheStringBytes {
		return
	}
	value.Active = false
	listMetadataCache.Lock()
	defer listMetadataCache.Unlock()
	listMetadataCache.clock++
	listMetadataCache.entries[key] = metadataCacheEntry{value: value, files: files, lastUse: listMetadataCache.clock}
	for len(listMetadataCache.entries) > maxMetadataCacheEntries {
		var oldestKey metadataCacheKey
		oldest := ^uint64(0)
		for candidate, entry := range listMetadataCache.entries {
			if entry.lastUse < oldest {
				oldestKey, oldest = candidate, entry.lastUse
			}
		}
		delete(listMetadataCache.entries, oldestKey)
	}
}

func cacheMetadataIfUnchanged(key metadataCacheKey, before metadataFiles, value Session) {
	currentKey, after, cacheable := metadataCacheIdentity(key.dir, key.id, before.updatedAt)
	if !cacheable || currentKey != key || !sameMetadataFiles(before, after) {
		return
	}
	cacheMetadata(key, after, value)
}

func invalidateMetadataCache(sessionsDir, id string) {
	dir := filepath.Clean(sessionsDir)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = filepath.Clean(resolved)
	}
	listMetadataCache.Lock()
	delete(listMetadataCache.entries, metadataCacheKey{dir: dir, id: id})
	listMetadataCache.Unlock()
}
