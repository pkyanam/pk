package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// locateBun honors PATH first, then Bun's explicit and standard user installs.
// Terminal apps, task runners, and reloads do not always load shell startup files.
func locateBun() (string, error) {
	if path, err := exec.LookPath("bun"); err == nil {
		return path, nil
	}
	candidates := []string{}
	if root := os.Getenv("BUN_INSTALL"); filepath.IsAbs(root) {
		candidates = append(candidates, filepath.Join(root, "bin", "bun"))
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		candidates = append(candidates, filepath.Join(home, ".bun", "bin", "bun"))
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			if resolved, err := exec.LookPath(path); err == nil {
				return resolved, nil
			}
		}
	}
	return "", fmt.Errorf("Bun was not found in PATH, BUN_INSTALL/bin, or ~/.bun/bin; install Bun or use --plain")
}
