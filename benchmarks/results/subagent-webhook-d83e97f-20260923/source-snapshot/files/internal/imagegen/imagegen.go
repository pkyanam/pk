// Package imagegen delegates image generation to Codex CLI's built-in tool.
// It deliberately treats Codex as an external image provider: the caller's
// normal pk model is not changed by this package.
package imagegen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxPromptBytes = 16 << 10
	maxImageBytes  = 32 << 20
	maxEventBytes  = 1 << 20
	maxImagePixels = 32_000_000
	maxImageSide   = 8192
)

// Config controls the separate Codex CLI image-generation worker.
type Config struct {
	Executable  string        // Defaults to codex on PATH.
	CodexHome   string        // Defaults to ~/.codex; never read for authentication.
	Driver      string        // Must be explicitly configured; separate from pk's model.
	DriverModel string        // Deprecated alias for Driver.
	Effort      string        // Defaults to low.
	Timeout     time.Duration // Defaults to 5 minutes.
	TempRoot    string        // Defaults to os.TempDir().
}

type Request struct {
	Prompt     string
	OutputRoot string   // Existing workspace root.
	OutputPath string   // New, relative path under OutputRoot; .png required.
	References []string // Explicit image paths, attached to the initial prompt.
}

type Result struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	MIME   string `json:"mime"`
}

type event struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Item     json.RawMessage `json:"item"`
	Payload  json.RawMessage `json:"payload"`
}

// Generate asks a Codex model to use its built-in image-generation tool. The
// command runs in a fresh workspace-write sandbox with shell execution disabled.
// Codex CLI currently saves generated images under CODEX_HOME/generated_images,
// so this method accepts only an artifact in the exact thread directory emitted
// by that invocation and copies it to a caller-selected, non-existing workspace path.
func Generate(ctx context.Context, cfg Config, req Request) (Result, error) {
	var zero Result
	if strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > maxPromptBytes {
		return zero, fmt.Errorf("prompt must be between 1 and %d bytes", maxPromptBytes)
	}
	driver, err := cfg.driverModel()
	if err != nil {
		return zero, err
	}
	if strings.TrimSpace(driver) == "" {
		return zero, errors.New("image-generation driver model must be configured explicitly")
	}
	if cfg.Effort == "" {
		cfg.Effort = "low"
	}
	if cfg.Effort != "low" && cfg.Effort != "medium" && cfg.Effort != "high" && cfg.Effort != "xhigh" && cfg.Effort != "max" && cfg.Effort != "ultra" {
		return zero, fmt.Errorf("unsupported reasoning effort %q", cfg.Effort)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.Executable == "" {
		cfg.Executable = "codex"
	}
	if cfg.CodexHome == "" {
		cfg.CodexHome = os.Getenv("CODEX_HOME")
		if cfg.CodexHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return zero, err
			}
			cfg.CodexHome = filepath.Join(home, ".codex")
		}
	}
	cfg.CodexHome, err = filepath.Abs(cfg.CodexHome)
	if err != nil {
		return zero, fmt.Errorf("resolve Codex home: %w", err)
	}
	outputPath, err := safeNewOutput(req.OutputRoot, req.OutputPath)
	if err != nil {
		return zero, err
	}
	if len(req.References) > 5 {
		return zero, errors.New("at most five reference images are supported")
	}
	var referenceBytes int64
	referencePaths := make([]string, 0, len(req.References))
	for _, ref := range req.References {
		p, err := filepath.Abs(ref)
		if err != nil {
			return zero, err
		}
		p, err = filepath.EvalSymlinks(p)
		if err != nil {
			return zero, fmt.Errorf("resolve reference image: %w", err)
		}
		st, err := os.Stat(p)
		if err != nil {
			return zero, fmt.Errorf("reference image: %w", err)
		}
		if !st.Mode().IsRegular() {
			return zero, fmt.Errorf("reference image must be a regular file: %s", p)
		}
		if st.Size() > 16<<20 {
			return zero, fmt.Errorf("reference image exceeds 16 MiB: %s", p)
		}
		referenceBytes += st.Size()
		if referenceBytes > 32<<20 {
			return zero, errors.New("reference images exceed the 32 MiB combined limit")
		}
		referencePaths = append(referencePaths, p)
	}

	tempRoot := cfg.TempRoot
	if tempRoot == "" {
		tempRoot = os.TempDir()
	}
	work, err := os.MkdirTemp(tempRoot, "pk-imagegen-*")
	if err != nil {
		return zero, err
	}
	defer os.RemoveAll(work)
	args := []string{"exec", "--ignore-user-config", "--skip-git-repo-check", "--model", driver, "-c", `model_reasoning_effort="` + cfg.Effort + `"`, "--sandbox", "workspace-write", "--disable", "shell_tool", "--ephemeral", "--json"}
	for index, source := range referencePaths {
		staged, err := stageReference(work, source, index)
		if err != nil {
			return zero, err
		}
		args = append(args, "--image", staged)
	}
	prompt := "Use the built-in image generation tool to create exactly one PNG image from this prompt: " + req.Prompt + ". Save the generated image artifact. Do not use shell, edit code, or perform any unrelated actions."
	if len(referencePaths) > 0 {
		prompt += " Use the attached reference image or images as visual references in the generation."
	}
	args = append(args, prompt)
	workerCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(workerCtx, cfg.Executable, args...)
	cmd.Dir = work
	cmd.Env = workerEnvironment(cfg.CodexHome)
	cmd.Stderr = &limitedBuffer{limit: maxEventBytes}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return zero, err
	}
	if err := startProcessGroup(cmd); err != nil {
		return zero, err
	}
	workerDone := make(chan struct{})
	defer close(workerDone)
	go func() {
		select {
		case <-workerCtx.Done():
			_ = killProcessGroup(cmd)
		case <-workerDone:
		}
	}()
	threadID := ""
	var streamErr error
	reader := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, readErr := readBoundedLine(reader, maxEventBytes)
		if len(line) != 0 {
			var e event
			if err := json.Unmarshal(line, &e); err != nil {
				streamErr = errors.New("invalid JSONL event from Codex CLI")
				_ = killProcessGroup(cmd)
				break
			}
			if e.Type == "thread.started" {
				if !validThreadID(e.ThreadID) {
					streamErr = errors.New("Codex CLI reported an invalid thread ID")
					_ = killProcessGroup(cmd)
					break
				}
				threadID = e.ThreadID
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				streamErr = readErr
				_ = killProcessGroup(cmd)
			}
			break
		}
	}
	waitErr := waitProcessGroup(cmd)
	if streamErr != nil {
		return zero, fmt.Errorf("read Codex JSONL: %w", streamErr)
	}
	if workerCtx.Err() != nil {
		return zero, workerCtx.Err()
	}
	if waitErr != nil {
		return zero, fmt.Errorf("Codex image worker failed: %w", waitErr)
	}
	if threadID == "" {
		return zero, errors.New("Codex CLI did not report a thread ID")
	}

	generatedDir := filepath.Join(cfg.CodexHome, "generated_images", threadID)
	dirInfo, err := os.Lstat(generatedDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return zero, errors.New("Codex generated-images thread directory is missing or not a regular directory")
	}
	entries, err := os.ReadDir(generatedDir)
	if err != nil {
		return zero, fmt.Errorf("Codex generated no accessible image artifact: %w", err)
	}
	var source string
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".png" {
			continue
		}
		info, err := os.Lstat(filepath.Join(generatedDir, ent.Name()))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return zero, errors.New("Codex image artifact is not a regular file")
		}
		if source != "" {
			return zero, errors.New("Codex produced multiple images; refusing ambiguous artifact selection")
		}
		source = filepath.Join(generatedDir, ent.Name())
	}
	if source == "" {
		return zero, errors.New("Codex completed without a PNG artifact")
	}
	img, err := os.Open(source)
	if err != nil {
		return zero, err
	}
	defer img.Close()
	st, err := img.Stat()
	if err != nil {
		return zero, err
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > maxImageBytes {
		return zero, errors.New("generated artifact is not a regular PNG within the 32 MiB limit")
	}
	config, err := png.DecodeConfig(io.LimitReader(img, maxImageBytes))
	if err != nil {
		return zero, fmt.Errorf("generated artifact is not a valid PNG: %w", err)
	}
	if _, err := img.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxImageSide || config.Height > maxImageSide || int64(config.Width)*int64(config.Height) > maxImagePixels {
		return zero, errors.New("generated PNG dimensions exceed safe limits")
	}
	if _, err := png.Decode(io.LimitReader(img, maxImageBytes)); err != nil {
		return zero, fmt.Errorf("generated PNG is incomplete or corrupt: %w", err)
	}
	if _, err := img.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	if err := copyToWorkspace(req.OutputRoot, req.OutputPath, outputPath, img, st.Size()); err != nil {
		return zero, err
	}
	return Result{Path: outputPath, Bytes: st.Size(), Width: config.Width, Height: config.Height, MIME: "image/png"}, nil
}

func (cfg Config) driverModel() (string, error) {
	if cfg.Driver != "" && cfg.DriverModel != "" && cfg.Driver != cfg.DriverModel {
		return "", errors.New("conflicting image-generation driver settings")
	}
	if cfg.Driver != "" {
		return cfg.Driver, nil
	}
	return cfg.DriverModel, nil
}

func workerEnvironment(codexHome string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "LANG": true, "LC_ALL": true, "SystemRoot": true}
	var env []string
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			env = append(env, key+"="+value)
		}
	}
	if os.Getenv("HOME") == "" {
		if home, err := os.UserHomeDir(); err == nil {
			env = append(env, "HOME="+home)
		}
	}
	return append(env, "CODEX_HOME="+codexHome)
}

func stageReference(work, source string, index int) (string, error) {
	before, err := os.Lstat(source)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("reference image is not a regular file: %s", source)
	}
	if before.Size() > 16<<20 {
		return "", fmt.Errorf("reference image exceeds 16 MiB: %s", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	after, err := in.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return "", fmt.Errorf("reference image changed during validation: %s", source)
	}
	header := make([]byte, 512)
	n, readErr := in.Read(header)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", readErr
	}
	mime := http.DetectContentType(header[:n])
	extension := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}[mime]
	if extension == "" {
		return "", fmt.Errorf("reference is not a supported PNG, JPEG, or WebP image: %s", source)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	dest := filepath.Join(work, fmt.Sprintf("reference-%d%s", index, extension))
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	count, copyErr := io.Copy(out, io.LimitReader(in, (16<<20)+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || count != after.Size() || count > 16<<20 {
		_ = os.Remove(dest)
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return "", fmt.Errorf("reference image changed or exceeded its size limit: %s", source)
	}
	return dest, nil
}

func copyToWorkspace(root, relative, expectedPath string, source io.Reader, expectedSize int64) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return err
	}
	clean := filepath.Clean(relative)
	rootFS, err := os.OpenRoot(rootAbs)
	if err != nil {
		return err
	}
	defer rootFS.Close()
	parent := filepath.Dir(clean)
	if parent != "." {
		if err := rootFS.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	out, err := rootFS.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create output without overwriting: %w", err)
	}
	n, copyErr := io.Copy(out, io.LimitReader(source, maxImageBytes+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || n != expectedSize || n > maxImageBytes {
		_ = rootFS.Remove(clean)
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return errors.New("generated image changed or exceeded the output size limit")
	}
	if expectedPath != filepath.Join(rootAbs, clean) {
		_ = rootFS.Remove(clean)
		return errors.New("resolved output path changed")
	}
	return nil
}

func safeNewOutput(root, relative string) (string, error) {
	if root == "" || relative == "" || filepath.IsAbs(relative) || filepath.Ext(relative) != ".png" {
		return "", errors.New("output requires an existing workspace root and a relative .png path")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("output path escapes workspace")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(rootAbs)
	if err != nil || !st.IsDir() {
		return "", errors.New("output root must be an existing directory")
	}
	dest := filepath.Join(rootAbs, clean)
	if rel, err := filepath.Rel(rootAbs, dest); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("output path escapes workspace")
	}
	for p := filepath.Dir(dest); p != rootAbs; p = filepath.Dir(p) {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("output path traverses a symlink")
		}
	}
	if fi, err := os.Lstat(dest); err == nil {
		_ = fi
		return "", errors.New("output path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return dest, nil
}

func validThreadID(s string) bool {
	if len(s) < 16 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func readBoundedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(out)+len(part) > limit {
			return nil, errors.New("Codex JSONL event exceeded size limit")
		}
		out = append(out, part...)
		if err == nil {
			return bytes.TrimSpace(out), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimSpace(out), err
	}
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < b.limit {
		keep := b.limit - b.Len()
		if keep > len(p) {
			keep = len(p)
		}
		_, _ = b.Buffer.Write(p[:keep])
	}
	return n, nil
}
