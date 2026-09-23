package main

import (
	"encoding/json"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/unreallabsai/unreal-agent/harness/operation"
)

const maxGeneratedImageBytes = 32 << 20
const maxGeneratedImageSide = 8192
const maxGeneratedImagePixels = 32_000_000

type generatedImage struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	MIME   string `json:"mime"`
}

// generatedImageFromOperation accepts only a completed ImageGen remote job and
// revalidates its artifact on disk before exposing a workspace-relative path.
func generatedImageFromOperation(current operation.Operation, workspace string) (generatedImage, bool) {
	if current.Type != operation.TypeRemoteJob || current.Status != operation.StatusCompleted || workspace == "" {
		return generatedImage{}, false
	}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil || state.Plan.Type != "pk.imagegen" {
		return generatedImage{}, false
	}
	var candidate generatedImage
	if json.Unmarshal([]byte(state.TerminalResult), &candidate) != nil || candidate.MIME != "image/png" || candidate.Width <= 0 || candidate.Height <= 0 || candidate.Width > maxGeneratedImageSide || candidate.Height > maxGeneratedImageSide || int64(candidate.Width)*int64(candidate.Height) > maxGeneratedImagePixels {
		return generatedImage{}, false
	}
	return validateGeneratedImage(candidate, workspace)
}

func validateGeneratedImage(candidate generatedImage, workspace string) (generatedImage, bool) {
	if candidate.MIME != "image/png" || candidate.Width <= 0 || candidate.Height <= 0 || candidate.Width > maxGeneratedImageSide || candidate.Height > maxGeneratedImageSide || int64(candidate.Width)*int64(candidate.Height) > maxGeneratedImagePixels {
		return generatedImage{}, false
	}
	if candidate.Path == "" || len(candidate.Path) > 1024 {
		return generatedImage{}, false
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return generatedImage{}, false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return generatedImage{}, false
	}
	inputPath := filepath.Clean(filepath.FromSlash(candidate.Path))
	var rel string
	if filepath.IsAbs(inputPath) {
		resolvedInput, err := filepath.EvalSymlinks(inputPath)
		if err != nil {
			return generatedImage{}, false
		}
		rel, err = filepath.Rel(root, resolvedInput)
		if err != nil {
			return generatedImage{}, false
		}
	} else {
		rel = inputPath
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return generatedImage{}, false
	}
	path := filepath.Join(root, rel)
	// Reject symlink traversal, even when the link points back inside the
	// workspace. The UI receives a stable artifact owned by this workspace.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return generatedImage{}, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxGeneratedImageBytes {
		return generatedImage{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return generatedImage{}, false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return generatedImage{}, false
	}
	config, err := png.DecodeConfig(io.LimitReader(f, maxGeneratedImageBytes))
	if err != nil || config.Width != candidate.Width || config.Height != candidate.Height {
		return generatedImage{}, false
	}
	candidate.Path = filepath.ToSlash(rel)
	return candidate, true
}
