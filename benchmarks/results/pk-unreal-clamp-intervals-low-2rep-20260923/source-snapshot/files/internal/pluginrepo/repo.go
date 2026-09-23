// Package pluginrepo discovers and installs explicit pk.extensions/v1
// components from local repositories or public GitHub repositories. Discovery
// reads manifest metadata only and never starts a plugin worker.
package pluginrepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pkyanam/pk/internal/extensions"
)

const (
	maxFiles       = 10000
	maxTreeBytes   = 64 << 20
	maxFileBytes   = 16 << 20
	maxOutputBytes = 64 << 10
)

var ownerRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type Candidate struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	ManifestPath  string   `json:"manifest_path"`
	Tools         []string `json:"tools,omitempty"`
	Commands      []string `json:"commands,omitempty"`
	BuildRequired bool     `json:"build_required,omitempty"`
}

type Unsupported struct {
	Path   string `json:"path"`
	Format string `json:"format"`
	Reason string `json:"reason"`
}

type Catalog struct {
	Source      string        `json:"source"`
	Revision    string        `json:"revision,omitempty"`
	Candidates  []Candidate   `json:"candidates"`
	Unsupported []Unsupported `json:"unsupported,omitempty"`
}

type Installed struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	ManifestPath string   `json:"manifest_path"`
	Source       string   `json:"source"`
	Revision     string   `json:"revision"`
	Tools        []string `json:"tools,omitempty"`
	Commands     []string `json:"commands,omitempty"`
}

type Installer struct {
	Home string
	// Command is injectable in tests. It must execute argv directly, never via
	// a shell. Nil uses exec.CommandContext.
	Command func(context.Context, string, string, ...string) ([]byte, error)
}

type source struct {
	root      string
	canonical string
	revision  string
	cleanup   func()
}

func Discover(ctx context.Context, raw string) (Catalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := openSource(ctx, raw, defaultCommand)
	if err != nil {
		return Catalog{}, err
	}
	defer resolved.cleanup()
	return catalogAt(resolved)
}

func (i Installer) Install(ctx context.Context, rawSource, manifestPath, expectedRevision string) (Installed, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(i.Home) == "" {
		return Installed{}, errors.New("pk home directory is required")
	}
	if err := os.MkdirAll(i.Home, 0o700); err != nil {
		return Installed{}, fmt.Errorf("create pk home directory: %w", err)
	}
	if err := os.Chmod(i.Home, 0o700); err != nil {
		return Installed{}, fmt.Errorf("secure pk home directory: %w", err)
	}
	command := i.Command
	if command == nil {
		command = defaultCommand
	}
	resolved, err := openSource(ctx, rawSource, command)
	if err != nil {
		return Installed{}, err
	}
	defer resolved.cleanup()
	if expectedRevision != "" && resolved.revision != expectedRevision {
		return Installed{}, fmt.Errorf("source changed after preview (revision %s, expected %s); discover it again", resolved.revision, expectedRevision)
	}
	catalog, err := catalogAt(resolved)
	if err != nil {
		return Installed{}, err
	}
	var candidate *Candidate
	for index := range catalog.Candidates {
		if catalog.Candidates[index].ManifestPath == filepath.ToSlash(filepath.Clean(manifestPath)) {
			candidate = &catalog.Candidates[index]
			break
		}
	}
	if candidate == nil {
		return Installed{}, errors.New("selected pk extension manifest was not found in this source")
	}
	manifestAbsolute := filepath.Join(resolved.root, filepath.FromSlash(candidate.ManifestPath))
	manifest, err := extensions.LoadManifest(manifestAbsolute)
	if err != nil {
		return Installed{}, fmt.Errorf("load selected extension manifest: %w", err)
	}
	component := filepath.Dir(filepath.FromSlash(candidate.ManifestPath))
	if component == "" {
		component = "."
	}
	componentSource := filepath.Join(resolved.root, component)
	if err := ensureWithin(resolved.root, componentSource); err != nil {
		return Installed{}, err
	}
	pluginsRoot := filepath.Join(i.Home, "plugins")
	rootInfo, err := os.Lstat(pluginsRoot)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(pluginsRoot, 0o700); err != nil {
			return Installed{}, fmt.Errorf("create managed plugin directory: %w", err)
		}
		rootInfo, err = os.Lstat(pluginsRoot)
	}
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return Installed{}, errors.New("managed plugin root must be a real directory")
	}
	if err := os.Chmod(pluginsRoot, 0o700); err != nil {
		return Installed{}, err
	}
	destination := filepath.Join(pluginsRoot, manifest.ID)
	if _, err := os.Lstat(destination); err == nil {
		return Installed{}, fmt.Errorf("managed plugin %q is already installed; remove it before installing another revision", manifest.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Installed{}, err
	}
	stage, err := os.MkdirTemp(pluginsRoot, ".install-")
	if err != nil {
		return Installed{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return Installed{}, err
	}
	componentStage := filepath.Join(stage, component)
	if err := copyTree(componentSource, componentStage); err != nil {
		return Installed{}, fmt.Errorf("copy selected extension component: %w", err)
	}
	var raw struct {
		Executable string `json:"executable"`
	}
	manifestData, err := os.ReadFile(manifestAbsolute)
	if err != nil {
		return Installed{}, err
	}
	if err := json.Unmarshal(manifestData, &raw); err != nil {
		return Installed{}, err
	}
	executable := strings.TrimSpace(raw.Executable)
	if executable == "" {
		return Installed{}, errors.New("selected extension has no executable")
	}
	if !filepath.IsAbs(executable) && (strings.ContainsRune(executable, '/') || strings.ContainsRune(executable, filepath.Separator)) {
		executableSource := filepath.Join(componentSource, executable)
		if err := ensureWithin(componentSource, filepath.Clean(executableSource)); err != nil {
			return Installed{}, fmt.Errorf("extension worker path is outside its component: %w", err)
		}
		if _, err := os.Stat(executableSource); errors.Is(err, os.ErrNotExist) {
			if err := i.buildGoWorker(ctx, command, resolved.root, component, componentStage, executable); err != nil {
				return Installed{}, err
			}
		} else if err != nil {
			return Installed{}, fmt.Errorf("inspect declared worker: %w", err)
		}
	}
	provenance := struct {
		Source       string    `json:"source"`
		Revision     string    `json:"revision"`
		ManifestPath string    `json:"manifest_path"`
		InstalledAt  time.Time `json:"installed_at"`
	}{resolved.canonical, resolved.revision, candidate.ManifestPath, time.Now().UTC()}
	encoded, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return Installed{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, "provenance.json"), append(encoded, '\n'), 0o600); err != nil {
		return Installed{}, err
	}
	if err := os.Rename(stage, destination); err != nil {
		return Installed{}, fmt.Errorf("publish managed plugin: %w", err)
	}
	installedManifest := filepath.Join(destination, filepath.FromSlash(candidate.ManifestPath))
	return Installed{ID: manifest.ID, Version: manifest.Version, ManifestPath: installedManifest,
		Source: resolved.canonical, Revision: resolved.revision, Tools: toolNames(manifest), Commands: commandNames(manifest)}, nil
}

func (i Installer) buildGoWorker(ctx context.Context, command func(context.Context, string, string, ...string) ([]byte, error), root, component string, componentStage string, executable string) error {
	goMod := filepath.Join(root, "go.mod")
	if info, err := os.Stat(goMod); err != nil || !info.Mode().IsRegular() {
		return errors.New("declared extension worker is missing; the repository has no Go module at its root from which pk can build it")
	}
	componentDir := "."
	if component != "." {
		componentDir = "./" + filepath.ToSlash(component)
	}
	out := filepath.Join(componentStage, executable)
	if filepath.IsAbs(executable) || !filepath.IsLocal(executable) {
		return errors.New("missing worker path must be relative to the extension component")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := command(buildCtx, root, "go", "build", "-trimpath", "-o", out, componentDir); err != nil {
		return fmt.Errorf("build selected Go extension worker: %w", err)
	}
	return nil
}

func catalogAt(src source) (Catalog, error) {
	catalog := Catalog{Source: src.canonical, Revision: src.revision, Candidates: []Candidate{}}
	count := 0
	unsupported := []Unsupported{}
	err := filepath.WalkDir(src.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > maxFiles {
			return fmt.Errorf("source has more than %d entries", maxFiles)
		}
		rel, err := filepath.Rel(src.root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if skipRepositoryDirectory(rel) {
				return filepath.SkipDir
			}
			if filepath.ToSlash(rel) == ".pi/extensions" {
				unsupported = append(unsupported, Unsupported{Path: filepath.ToSlash(rel), Format: "Pi extension", Reason: "not a pk.extensions/v1 manifest; pk will not run or translate this format"})
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(rel)
		switch slash {
		case ".claude-plugin/plugin.json", ".claude-plugin/marketplace.json":
			unsupported = append(unsupported, Unsupported{Path: slash, Format: "Claude Code plugin", Reason: "not a pk.extensions/v1 manifest; pk will not execute or translate this format"})
		case "package.json":
			data, readErr := boundedRead(path, 256<<10)
			if readErr == nil && bytes.Contains(bytes.ToLower(data), []byte("\"pi\"")) {
				unsupported = append(unsupported, Unsupported{Path: slash, Format: "Pi package", Reason: "not a pk.extensions/v1 manifest; install through a supported adapter instead"})
			}
		}
		if filepath.Base(path) != "manifest.json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			return nil
		}
		manifest, err := extensions.LoadManifest(path)
		if err != nil {
			return nil // unrelated or unsupported manifest formats are not pk extensions
		}
		original, err := rawExecutable(path)
		if err != nil {
			return err
		}
		missing := false
		if !filepath.IsAbs(original) && (strings.ContainsRune(original, '/') || strings.ContainsRune(original, filepath.Separator)) {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), original)); errors.Is(err, os.ErrNotExist) {
				missing = true
			}
		}
		catalog.Candidates = append(catalog.Candidates, Candidate{ID: manifest.ID, Version: manifest.Version,
			ManifestPath: filepath.ToSlash(rel), Tools: toolNames(manifest), Commands: commandNames(manifest), BuildRequired: missing})
		return nil
	})
	if err != nil {
		return Catalog{}, fmt.Errorf("scan plugin source: %w", err)
	}
	sort.Slice(catalog.Candidates, func(i, j int) bool { return catalog.Candidates[i].ManifestPath < catalog.Candidates[j].ManifestPath })
	sort.Slice(unsupported, func(i, j int) bool { return unsupported[i].Path < unsupported[j].Path })
	catalog.Unsupported = unsupported
	return catalog, nil
}

func rawExecutable(path string) (string, error) {
	data, err := boundedRead(path, maxFileBytes)
	if err != nil {
		return "", err
	}
	var raw struct {
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	return raw.Executable, nil
}

func toolNames(manifest extensions.Manifest) []string {
	values := make([]string, 0, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		values = append(values, tool.Name)
	}
	return values
}

func commandNames(manifest extensions.Manifest) []string {
	values := make([]string, 0, len(manifest.Commands))
	for _, command := range manifest.Commands {
		values = append(values, command.Name)
	}
	return values
}

func openSource(ctx context.Context, raw string, command func(context.Context, string, string, ...string) ([]byte, error)) (source, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return source{}, errors.New("plugin source is required")
	}
	if info, err := os.Stat(raw); err == nil && info.IsDir() {
		root, err := filepath.Abs(raw)
		if err != nil {
			return source{}, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return source{}, err
		}
		if err := validateTreeSize(root); err != nil {
			return source{}, err
		}
		revision, err := localRevision(ctx, command, root)
		if err != nil {
			return source{}, err
		}
		return source{root: root, canonical: root, revision: revision, cleanup: func() {}}, nil
	}
	repository, err := normalizeRepository(raw)
	if err != nil {
		return source{}, err
	}
	temp, err := os.MkdirTemp("", "pk-plugin-source-")
	if err != nil {
		return source{}, err
	}
	_ = os.Remove(temp) // git clone requires a not-yet-created destination.
	cleanup := func() { _ = os.RemoveAll(temp) }
	cloneCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err = command(cloneCtx, "", "git", "-c", "core.hooksPath="+nullDevice(), "-c", "init.templateDir="+nullDevice(), "clone", "--depth=1", "--quiet", "--no-recurse-submodules", "--", repository, temp)
	if err != nil {
		cleanup()
		return source{}, fmt.Errorf("fetch plugin source: %w", err)
	}
	if err := validateTreeSize(temp); err != nil {
		cleanup()
		return source{}, err
	}
	revisionBytes, err := command(ctx, temp, "git", "rev-parse", "HEAD")
	if err != nil {
		cleanup()
		return source{}, fmt.Errorf("resolve plugin source revision: %w", err)
	}
	revision := strings.TrimSpace(string(revisionBytes))
	if !validRevision(revision) {
		cleanup()
		return source{}, errors.New("git returned an invalid source revision")
	}
	return source{root: temp, canonical: repository, revision: revision, cleanup: cleanup}, nil
}

func normalizeRepository(raw string) (string, error) {
	if ownerRepoPattern.MatchString(raw) {
		return "https://github.com/" + strings.TrimSuffix(raw, ".git") + ".git", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("source must be a local repository directory, GitHub owner/repo, or an HTTPS GitHub repository URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("GitHub source URL must point to a repository root")
	}
	name := strings.TrimSuffix(parts[1], ".git")
	if name == "" {
		return "", errors.New("GitHub repository name is empty")
	}
	return "https://github.com/" + parts[0] + "/" + name + ".git", nil
}

func validateTreeSize(root string) error {
	count, size := 0, int64(0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > maxFiles {
			return fmt.Errorf("plugin source exceeds %d filesystem entries", maxFiles)
		}
		if entry.IsDir() {
			rel, _ := filepath.Rel(root, path)
			if skipRepositoryDirectory(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil // preview never follows symlinks; selected components reject them.
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("plugin source contains a non-regular file")
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("plugin source file %q exceeds %d bytes", path, maxFileBytes)
		}
		size += info.Size()
		if size > maxTreeBytes {
			return fmt.Errorf("plugin source exceeds %d bytes", maxTreeBytes)
		}
		return nil
	})
	return err
}

func copyTree(source, destination string) error {
	count, total := 0, int64(0)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > maxFiles {
			return fmt.Errorf("component exceeds %d filesystem entries", maxFiles)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("component contains symlink %q", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("component contains non-regular file %q", rel)
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("component file %q exceeds %d bytes", rel, maxFileBytes)
		}
		total += info.Size()
		if total > maxTreeBytes {
			return fmt.Errorf("component exceeds %d bytes", maxTreeBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		mode := info.Mode().Perm() & 0o755
		if err := copyFile(path, target, mode); err != nil {
			return err
		}
		return nil
	})
}

func copyFile(source, destination string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	copied, copyErr := io.Copy(out, io.LimitReader(in, maxFileBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if copied > maxFileBytes {
		return fmt.Errorf("source file %q exceeds %d bytes", source, maxFileBytes)
	}
	return nil
}

func boundedRead(path string, max int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file %q exceeds %d bytes", path, max)
	}
	return data, nil
}

func ensureWithin(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path escapes its source directory")
	}
	return nil
}

func localRevision(ctx context.Context, command func(context.Context, string, string, ...string) ([]byte, error), root string) (string, error) {
	data, err := command(ctx, root, "git", "rev-parse", "HEAD")
	if err == nil {
		revision := strings.TrimSpace(string(data))
		if validRevision(revision) {
			return revision, nil
		}
	}
	// Non-Git local folders still receive stable provenance from the selected
	// manifest tree content; no external commands or files are executed.
	hash := sha256.New()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() && skipRepositoryDirectory(rel) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(rel)+"\x00")
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(hash, io.LimitReader(file, maxFileBytes+1))
		_ = file.Close()
		return copyErr
	}); err != nil {
		return "", err
	}
	return "local:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func skipRepositoryDirectory(rel string) bool {
	rel = filepath.ToSlash(rel)
	return rel == ".git" || strings.HasPrefix(rel, ".git/") || rel == "node_modules" || strings.HasPrefix(rel, "node_modules/") || strings.Contains(rel, "/node_modules/") || strings.HasSuffix(rel, "/node_modules")
}

func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func defaultCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if name == "git" {
		command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+nullDevice())
	} else if name == "go" {
		// Keep toolchain selection under the user's Go installation/configuration.
		// In particular, newer modules may require Go's automatic toolchain
		// download when the locally installed launcher is older.
		command.Env = append(command.Env, "GOWORK=off", "GOFLAGS=")
	}
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxOutputBytes, maxOutputBytes
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, detail)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.Buffer.Write(data)
	}
	return original, nil
}

func nullDevice() string {
	if runtime.GOOS == "windows" {
		return "NUL"
	}
	return os.DevNull
}
