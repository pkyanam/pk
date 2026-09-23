package providers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModelCatalogCacheBindsBaseURLAndPreservesLimits(t *testing.T) {
	store := Store{Home: filepath.Join(t.TempDir(), "pk")}
	fetchedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	models := []Model{{ID: "model-a", ContextTokens: cacheInt64(128_000), InputTokens: cacheInt64(96_000), OutputTokens: cacheInt64(8_000)}}
	if err := store.SaveModelCatalog("team", "https://example.test/v1/", models, fetchedAt); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.LoadModelCatalog("team", "https://example.test")
	if err != nil || !found {
		t.Fatalf("load catalog = (%+v, %v, %v)", got, found, err)
	}
	if got.BaseURL != "https://example.test/v1" || !got.FetchedAt.Equal(fetchedAt) || len(got.Models) != 1 {
		t.Fatalf("catalog identity changed: %+v", got)
	}
	model := got.Models[0]
	if model.ContextTokens == nil || *model.ContextTokens != 128_000 || model.InputTokens == nil || *model.InputTokens != 96_000 || model.OutputTokens == nil || *model.OutputTokens != 8_000 || model.LimitsSource != "provider_cache" {
		t.Fatalf("cached model metadata = %+v", model)
	}
	if _, found, err := store.LoadModelCatalog("team", "https://other.test"); err != nil || found {
		t.Fatalf("catalog leaked across base URLs: found=%v err=%v", found, err)
	}
	info, err := os.Stat(store.modelCachePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestModelCatalogCacheRejectsUnsafeOrMalformedFile(t *testing.T) {
	dir := t.TempDir()
	store := Store{Home: dir}
	if err := os.WriteFile(store.modelCachePath(), []byte(`{"version":1,"catalogs":[{"provider_id":"x","base_url":"javascript://example.test","fetched_at":"2026-09-23T12:00:00Z","models":[{"id":"m","context_tokens":1000}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadModelCatalog("x", "https://example.test"); err == nil {
		t.Fatal("invalid cached limits accepted")
	}
	_ = os.Remove(store.modelCachePath())
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{"version":1,"catalogs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.modelCachePath()); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := store.LoadModelCatalog("x", "https://example.test"); err == nil {
		t.Fatal("symlink cache accepted")
	}
}

func cacheInt64(value int64) *int64 { return &value }
