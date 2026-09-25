// Package ext implements the v2 extension layer: JS/TS plugins run under
// Bun, with plugin files watched for create/edit/delete and live reload
// (see docs/v2-architecture.md, "Extension layer (Bun/JS-TS)").
//
// M1 scope is deliberately small and stdlib-only: one package that watches a
// plugins directory for a single TS file (plugin.ts), runs it under Bun to
// collect a JSON manifest of registered tools, and hot-reloads whenever the
// file is created, edited, or deleted. This is the "write a tool → invoke it
// the same turn" demo: editing plugin.ts is picked up on the next poll, and
// the refreshed manifest is published to the caller via the onChange hook.
//
// Bun is optional. When it is absent the package degrades gracefully:
// LoadPlugin returns BunNotFound and WatchDir reports it through onChange,
// leaving the daemon with core tools only.
package ext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// PluginFile is the single TS file this M1 milestone watches inside a
// plugins directory. A later milestone generalizes to many plugins and to
// create/edit/delete of arbitrary files (opencode-compatible hooks).
const PluginFile = "plugin.ts"

// BunNotFound is returned by LoadPlugin (and reported through WatchDir) when
// the Bun runtime is not installed or not on PATH. Callers should treat this
// as the signal to degrade to core tools.
var BunNotFound = errors.New("ext: bun not available")

// bunLookup resolves the bun executable. It is a var so tests can inject a
// fake "bun absent" environment without mutating PATH.
var bunLookup = exec.LookPath

// BunAvailable reports whether the Bun runtime can be found on PATH.
func BunAvailable() bool {
	_, err := bunLookup("bun")
	return err == nil
}

// Tool is a single tool registered by a plugin. Parameters is a JSON-schema
// object describing the tool's arguments (type, properties, required, ...).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Manifest is the JSON manifest of tools a plugin registers. The Bun script
// prints it as a single JSON line — `{"tools":[...]}` — and LoadPlugin
// parses it back into Go values.
type Manifest struct {
	Tools []Tool `json:"tools"`
}

// LoadPlugin shells out to `bun run <dir>/plugin.ts` and parses the printed
// JSON manifest of registered tools. The plugin contract (see
// testdata/plugin.ts): when run directly, the script prints exactly one
// JSON object with a "tools" array. When Bun is absent, LoadPlugin returns
// BunNotFound and a zero Manifest, so callers can degrade to core tools.
func LoadPlugin(dir string) (Manifest, error) {
	bun, err := bunLookup("bun")
	if err != nil {
		return Manifest{}, BunNotFound
	}
	script := filepath.Join(dir, PluginFile)
	if _, err := os.Stat(script); err != nil {
		return Manifest{}, fmt.Errorf("ext: plugin %s: %w", script, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bun, "run", script)
	out, err := cmd.Output()
	if err != nil {
		return Manifest{}, fmt.Errorf("ext: bun run %s: %w", script, err)
	}
	var m Manifest
	if err := json.Unmarshal(bytes.TrimSpace(out), &m); err != nil {
		return Manifest{}, fmt.Errorf("ext: parse manifest from %s: %w", script, err)
	}
	return m, nil
}

// WatchDir polls dir for PluginFile and re-runs LoadPlugin whenever the file
// is created, edited, or deleted, reporting every result — errors included,
// so a missing plugin or absent Bun surfaces as a (Manifest{}, error) pair
// and the caller can degrade to core tools. onChange is always called from a
// single goroutine, never concurrently with itself; the first call happens
// shortly after WatchDir starts and reflects the current state of plugin.ts
// (BunNotFound if Bun is absent). WatchDir runs until the process exits; use
// WatchDirContext to stop early.
func WatchDir(dir string, onChange func(Manifest, error)) {
	WatchDirContext(context.Background(), dir, onChange)
}

// WatchDirContext is WatchDir with a stop signal: polling ends when ctx is
// cancelled. Transient errors never stop the loop; only a cancelled context
// does.
func WatchDirContext(ctx context.Context, dir string, onChange func(Manifest, error)) {
	watchDir(ctx, dir, onChange, pollInterval)
}

// watchDir is the shared poll loop. interval is a parameter so tests can
// speed the loop up without mutating a package global (which would race with
// a running loop); WatchDirContext passes the package default.
func watchDir(ctx context.Context, dir string, onChange func(Manifest, error), interval time.Duration) {
	script := filepath.Join(dir, PluginFile)
	last := modStamp{}
	first := true
	for {
		stamp := statStamp(script)
		if first || !stamp.equal(last) {
			first = false
			last = stamp
			m, err := LoadPlugin(dir)
			onChange(m, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// pollInterval is how often WatchDir re-stats plugin.ts. A var so tests can
// speed it up; the demo default is fine for a daemon.
var pollInterval = 100 * time.Millisecond

// modStamp captures enough of a file's state to detect create/edit/delete
// without fsnotify: existence plus modification time and size.
type modStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

func statStamp(path string) modStamp {
	info, err := os.Stat(path)
	if err != nil {
		return modStamp{}
	}
	return modStamp{exists: true, modTime: info.ModTime(), size: info.Size()}
}

// equal reports whether two stamps describe the same file state.
func (s modStamp) equal(o modStamp) bool {
	if s.exists != o.exists {
		return false
	}
	if !s.exists {
		return true // both absent
	}
	return s.modTime.Equal(o.modTime) && s.size == o.size
}
