package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateBunWithoutShellStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("BUN_INSTALL", "")
	if _, err := locateBun(); err == nil {
		t.Fatal("found missing Bun")
	}
	write := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	standard := filepath.Join(home, ".bun", "bin", "bun")
	write(standard, 0600)
	if _, err := locateBun(); err == nil {
		t.Fatal("accepted nonexecutable Bun")
	}
	if err := os.Chmod(standard, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := locateBun(); err != nil || got != standard {
		t.Fatalf("standard: %q %v", got, err)
	}
	custom := filepath.Join(home, "custom")
	write(filepath.Join(custom, "bin", "bun"), 0700)
	t.Setenv("BUN_INSTALL", custom)
	if got, err := locateBun(); err != nil || got != filepath.Join(custom, "bin", "bun") {
		t.Fatalf("custom: %q %v", got, err)
	}
	preferred := filepath.Join(home, "preferred")
	write(filepath.Join(preferred, "bun"), 0700)
	t.Setenv("PATH", preferred)
	if got, err := locateBun(); err != nil || got != filepath.Join(preferred, "bun") {
		t.Fatalf("PATH: %q %v", got, err)
	}
}
