package contextbudget

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadCodexModelCacheUsesEffectiveWindowAndOnlyModelMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models_cache.json")
	const source = `{"fetched_at":"2026-09-23T17:16:14Z","identity":{"secret":"must-not-escape"},"models":[{"slug":"gpt-6-luna","context_window":272000,"max_context_window":872000,"effective_context_window_percent":95}]}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	limits, found, err := ReadCodexModelCache(path, "gpt-6-luna")
	if err != nil || !found {
		t.Fatalf("ReadCodexModelCache() = (%+v, %v, %v)", limits, found, err)
	}
	if *limits.ContextTokens != 272_000 || *limits.InputTokens != 258_400 || limits.OutputTokens != nil {
		t.Fatalf("unexpected native limits: %+v", limits)
	}
	if limits.Source != SourceCodexLocalCatalog || !limits.InputIncludesHeadroom || limits.FetchedAt.IsZero() {
		t.Fatalf("missing provenance/headroom: %+v", limits)
	}
	if limits.FetchedAt.UTC().Format(time.RFC3339) != "2026-09-23T17:16:14Z" {
		t.Fatalf("fetched_at = %s", limits.FetchedAt)
	}
	budget, err := Resolve("native", "gpt-6-luna", &limits, nil, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if budget.OperationalInputBudgetTokens != 242_904 || budget.OutputReserveTokens != 25_000 {
		t.Fatalf("configured reserve/margin did not constrain the effective input cap: %+v", budget)
	}
}

func TestReadCodexModelCacheFallsBackAndRejectsMalformedLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models_cache.json")
	if err := os.WriteFile(path, []byte(`{"models":[{"slug":"model","max_context_window":1000}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	limits, found, err := ReadCodexModelCache(path, "model")
	if err != nil || !found || *limits.ContextTokens != 1000 || *limits.InputTokens != 950 {
		t.Fatalf("fallback max context = %+v, %v, %v", limits, found, err)
	}
	if err := os.WriteFile(path, []byte(`{"models":[{"slug":"model","context_window":1000,"effective_context_window_percent":101}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCodexModelCache(path, "model"); err == nil {
		t.Fatal("invalid effective context percent accepted")
	}
}

func TestReadCodexModelCacheDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{"models":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := ReadCodexModelCache(link, "model"); err == nil {
		t.Fatal("symlink catalog accepted")
	}
}
