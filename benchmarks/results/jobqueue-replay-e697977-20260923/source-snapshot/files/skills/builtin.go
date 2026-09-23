// Package skills ships pk's own on-demand harness instructions with the binary.
package skills

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed pk/SKILL.md
var pkSkill []byte

// Materialize returns a discovery directory containing the version of the skill
// embedded in this binary. Content addressing keeps older session paths valid.
func Materialize(home string) (string, error) {
	digest := sha256.Sum256(pkSkill)
	root := filepath.Join(home, "bundled-skills", fmt.Sprintf("%x", digest[:12]))
	directory := filepath.Join(root, "pk")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "SKILL.md")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, pkSkill) {
		return root, nil
	}
	file, err := os.CreateTemp(directory, ".skill-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(pkSkill)
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return root, nil
}
