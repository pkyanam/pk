package ext

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// requireBun skips the test when the Bun runtime is not installed, so the
// suite still passes on machines without Bun (the graceful-degradation path
// is exercised separately by TestLoadPluginWithoutBun).
func requireBun(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not installed; skipping live Bun test")
	}
}

// fixtureDir returns a temp dir containing the testdata plugin.ts. The
// returned dir is NOT cleaned up, so tests can bump the file's modtime
// without racing a deferred RemoveAll.
func fixtureDir(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", PluginFile))
	if err != nil {
		t.Fatalf("read testdata plugin: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PluginFile), src, 0o644); err != nil {
		t.Fatalf("write plugin into temp dir: %v", err)
	}
	return dir
}

// TestLoadPluginParsesManifestRoundtrip runs the real Bun path: the script
// prints {"tools":[...]}, and the Go side must parse it back into the exact
// Tool values the plugin exported.
func TestLoadPluginParsesManifestRoundtrip(t *testing.T) {
	requireBun(t)
	dir := fixtureDir(t)

	m, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("LoadPlugin(dir) error = %v", err)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("len(m.Tools) = %d, want 1", len(m.Tools))
	}
	tool := m.Tools[0]
	if tool.Name != "pk2_echo" {
		t.Errorf("tool.Name = %q, want %q", tool.Name, "pk2_echo")
	}
	if !strings.Contains(tool.Description, "Echoes back") {
		t.Errorf("tool.Description = %q, want it to mention echo", tool.Description)
	}
	wantParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string", "description": "Text to echo back."},
		},
		"required": []any{"message"},
	}
	if !reflect.DeepEqual(tool.Parameters, wantParams) {
		t.Errorf("tool.Parameters = %#v, want %#v", tool.Parameters, wantParams)
	}

	// Roundtrip: re-marshaling must produce the same JSON shape the plugin
	// printed (stable, loadable again).
	again, err := LoadPlugin(dir)
	if err != nil {
		t.Fatalf("second LoadPlugin(dir) error = %v", err)
	}
	if !reflect.DeepEqual(again, m) {
		t.Errorf("second load = %#v, first = %#v; manifests differ across loads", again, m)
	}
}

// TestLoadPluginWithoutBun verifies graceful degradation: when Bun is
// absent, LoadPlugin returns BunNotFound instead of failing hard, so the
// daemon can fall back to core tools. The Bun lookup is injected to fake
// "no Bun" without touching the real PATH.
func TestLoadPluginWithoutBun(t *testing.T) {
	original := bunLookup
	bunLookup = func(string) (string, error) { return "", errors.New("bun: not found") }
	defer func() { bunLookup = original }()

	if BunAvailable() {
		t.Fatal("BunAvailable() = true with injected failing lookup")
	}
	m, err := LoadPlugin(".")
	if !errors.Is(err, BunNotFound) {
		t.Fatalf("LoadPlugin error = %v, want BunNotFound", err)
	}
	if len(m.Tools) != 0 {
		t.Errorf("manifest has %d tools on BunNotFound path, want 0", len(m.Tools))
	}
}

// TestLoadPluginMissingFile verifies a missing plugin file is reported as an
// error (distinct from BunNotFound), not silently swallowed.
func TestLoadPluginMissingFile(t *testing.T) {
	requireBun(t)
	m, err := LoadPlugin(t.TempDir())
	if err == nil {
		t.Fatal("LoadPlugin(empty dir) error = nil, want an error")
	}
	if errors.Is(err, BunNotFound) {
		t.Fatalf("missing plugin file surfaced as BunNotFound: %v", err)
	}
	if len(m.Tools) != 0 {
		t.Errorf("manifest has %d tools on error path, want 0", len(m.Tools))
	}
}

// TestWatchDirReloadsOnChange verifies hot reload: WatchDirContext must
// report the plugin's initial tools, then a re-loaded manifest after the
// plugin.ts file is edited (modtime bump) and after it is deleted.
func TestWatchDirReloadsOnChange(t *testing.T) {
	requireBun(t)

	dir := fixtureDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan result, 8)
	go watchDir(ctx, dir, func(m Manifest, err error) {
		results <- result{m: m, err: err}
	}, 5*time.Millisecond)

	// 1. Initial load: one tool.
	first := waitResult(t, results, "initial load")
	if first.err != nil {
		t.Fatalf("initial load error = %v", first.err)
	}
	if len(first.m.Tools) != 1 || first.m.Tools[0].Name != "pk2_echo" {
		t.Fatalf("initial manifest = %#v, want one pk2_echo tool", first.m)
	}

	// 2. Edit plugin.ts (append a comment, bump modtime): reload reports the
	//    same tools again.
	script := filepath.Join(dir, PluginFile)
	f, err := os.OpenFile(script, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open plugin for edit: %v", err)
	}
	if _, err := f.WriteString("\n// edited for reload test\n"); err != nil {
		t.Fatalf("edit plugin: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close plugin: %v", err)
	}
	// Ensure the modtime actually moves, even on coarse-resolution filesystems.
	touch(t, script, time.Now().Add(time.Second))

	// 2. Edit: the write bumps size and the explicit touch bumps modtime, so
	//    the watcher may report more than once; drain stragglers after the
	//    change so the channel is quiet before the next mutation.
	second := waitResult(t, results, "reload after edit")
	if second.err != nil {
		t.Fatalf("reload after edit error = %v", second.err)
	}
	if !reflect.DeepEqual(second.m, first.m) {
		t.Errorf("manifest after edit = %#v, want %#v", second.m, first.m)
	}
	drainReports(t, results)

	// 3. Delete plugin.ts: reload reports an error (missing file).
	if err := os.Remove(script); err != nil {
		t.Fatalf("remove plugin: %v", err)
	}
	third := waitResult(t, results, "reload after delete")
	if third.err == nil {
		t.Fatal("manifest after delete has nil error, want an error")
	}
	if len(third.m.Tools) != 0 {
		t.Errorf("manifest after delete has %d tools, want 0", len(third.m.Tools))
	}
	drainReports(t, results)

	// 4. No more changes: no further reports.
	select {
	case r := <-results:
		t.Fatalf("unexpected extra reload report: %#v", r)
	case <-time.After(5 * pollInterval):
	}
}

// TestWatchDirReportsBunNotFoundWithoutBun verifies the graceful-degradation
// path is also honored by WatchDir: with Bun injected as absent, the watcher
// keeps reporting BunNotFound instead of crashing.
func TestWatchDirReportsBunNotFoundWithoutBun(t *testing.T) {
	original := bunLookup
	bunLookup = func(string) (string, error) { return "", errors.New("bun: not found") }
	// Restore runs last (LIFO), after the watcher goroutine has exited, so
	// the loop never reads bunLookup concurrently with the restore.
	defer func() { bunLookup = original }()

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	results := make(chan result, 8)
	go func() {
		defer close(done)
		watchDir(ctx, dir, func(m Manifest, err error) {
			results <- result{m: m, err: err}
		}, 5*time.Millisecond)
	}()
	// Defer order matters (LIFO): cancel() runs first so the watcher stops,
	// then <-done waits for it to finish, and only then is bunLookup
	// restored — the loop never reads bunLookup concurrently with the
	// restore.
	defer func() { <-done }()
	defer cancel()

	r := waitResult(t, results, "bun-not-found report")
	if !errors.Is(r.err, BunNotFound) {
		t.Fatalf("WatchDir error = %v, want BunNotFound", r.err)
	}
}

// result is a single onChange delivery captured by the watch tests.
type result struct {
	m   Manifest
	err error
}

// waitResult waits for the next onChange delivery with a timeout.
func waitResult(t *testing.T, results <-chan result, what string) result {
	t.Helper()
	select {
	case r := <-results:
		return r
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return result{} // unreachable
	}
}

// touch rewrites path with a future modtime so coarse-resolution filesystems
// still see the change.
func touch(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// drainReports discards in-flight onChange reports for a short window so the
// channel is quiet before the next mutation. A single file mutation (e.g. an
// edit) can surface as several stamp changes (size bump, modtime bump), and
// the watcher reports each; this keeps the phases of the test deterministic.
func drainReports(t *testing.T, results <-chan result) {
	t.Helper()
	deadline := time.Now().Add(10 * pollInterval)
	for time.Now().Before(deadline) {
		select {
		case <-results:
			// discard stragglers
		case <-time.After(pollInterval):
		}
	}
}
