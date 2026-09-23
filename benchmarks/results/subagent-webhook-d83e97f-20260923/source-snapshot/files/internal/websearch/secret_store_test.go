package websearch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSecretStoreSaveLoadClearAndPermissions(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".pk")
	store := SecretStore{Home: home}
	secret := "tinyfish-test-key-do-not-print"
	if err := store.Save(secret); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got != secret {
		t.Fatalf("Load() = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(home, "websearch.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v, err=%v", info, err)
	}
	dirInfo, err := os.Stat(home)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("pk home mode = %v, err=%v", dirInfo, err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load()
	if err != nil || got != "" {
		t.Fatalf("Load() after Clear() = %q, %v", got, err)
	}
}

func TestResolveAPIKeyEnvironmentPrecedesPrivateStore(t *testing.T) {
	store := SecretStore{Home: filepath.Join(t.TempDir(), ".pk")}
	if err := store.Save("private-key"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINYFISH_API_KEY", "environment-key")
	key, source, err := ResolveAPIKey(store.Home)
	if err != nil || key != "environment-key" || source != "environment" {
		t.Fatalf("ResolveAPIKey() = (%q,%q,%v)", key, source, err)
	}
	t.Setenv("TINYFISH_API_KEY", "")
	key, source, err = ResolveAPIKey(store.Home)
	if err != nil || key != "private-key" || source != "local_store" {
		t.Fatalf("ResolveAPIKey() after clearing env = (%q,%q,%v)", key, source, err)
	}
}

func TestSecretStoreRejectsInvalidUnsafeOrInsecureStore(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".pk")
	store := SecretStore{Home: home}
	for _, value := range []string{"", strings.Repeat("x", maxStoredAPIKeyBytes+1), "bad\nkey"} {
		if err := store.Save(value); err == nil {
			t.Fatalf("Save(%q) unexpectedly succeeded", value)
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "websearch.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"api_key":"valid"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load() accepted group/world-readable credential file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"api_key":"valid","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load() accepted unknown fields")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing-target"), path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load() accepted symlink credential file")
	}
	if err := store.Clear(); err == nil {
		t.Fatal("Clear() removed a symlink instead of rejecting it")
	}
}

func TestSecretStoreConcurrentSavesLeaveValidFile(t *testing.T) {
	store := SecretStore{Home: filepath.Join(t.TempDir(), ".pk")}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Save("key-value"); err != nil {
				t.Errorf("Save() = %v", err)
			}
		}(i)
	}
	wg.Wait()
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load() after concurrent saves: %v", err)
	}
}
