package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

type sourceFileDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type sourceManifest struct {
	PKRevision string             `json:"pk_revision"`
	TreeSHA256 string             `json:"source_tree_sha256"`
	DiffSHA256 string             `json:"git_diff_sha256"`
	Files      []sourceFileDigest `json:"files"`
}

func writeSourceManifest(repo, output string) (sourceManifest, error) {
	manifest := sourceManifest{PKRevision: gitRevision(context.Background(), repo)}
	roots := []string{"cmd/pk", "cmd/pkbench", "internal/runner", "internal/auth", "benchmarks/tasks/clamp", "benchmarks/tasks/noisyrepo", "benchmarks/experiments", "go.mod", "go.sum"}
	var paths []string
	for _, root := range roots {
		path := filepath.Join(repo, root)
		info, err := os.Stat(path)
		if err != nil {
			return sourceManifest{}, fmt.Errorf("stat benchmark source %s: %w", root, err)
		}
		if !info.IsDir() {
			paths = append(paths, path)
			continue
		}
		err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return err
			}
			paths = append(paths, current)
			return nil
		})
		if err != nil {
			return sourceManifest{}, err
		}
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return sourceManifest{}, err
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return sourceManifest{}, err
		}
		digest := sha256.Sum256(data)
		digestHex := hex.EncodeToString(digest[:])
		manifest.Files = append(manifest.Files, sourceFileDigest{Path: filepath.ToSlash(rel), SHA256: digestHex})
		_, _ = fmt.Fprintf(hash, "%s\x00%s\n", filepath.ToSlash(rel), digestHex)
	}
	manifest.TreeSHA256 = hex.EncodeToString(hash.Sum(nil))
	diffArgs := append([]string{"diff", "--binary", "HEAD", "--"}, roots...)
	diff := exec.Command("git", diffArgs...)
	diff.Dir = repo
	diffData, err := diff.Output()
	if err != nil {
		return sourceManifest{}, fmt.Errorf("hash benchmark source diff: %w", err)
	}
	diffSum := sha256.Sum256(diffData)
	manifest.DiffSHA256 = hex.EncodeToString(diffSum[:])
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return sourceManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(output, "source-manifest.json"), append(encoded, '\n'), 0o600); err != nil {
		return sourceManifest{}, err
	}
	return manifest, nil
}
