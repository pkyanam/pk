// Package update stages complete pk releases and switches the current release
// through an atomic symlink. It deliberately does not discover, pull, or modify
// a source checkout; callers must choose the source directory explicitly.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const modulePath = "github.com/pkyanam/pk"

type CommandRunner interface {
	Run(ctx context.Context, directory, name string, stdout, stderr io.Writer, args ...string) error
}

type osCommandRunner struct{}

func (osCommandRunner) Run(ctx context.Context, directory, name string, stdout, stderr io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = buildEnvironment(os.Environ())
	command.Stdout, command.Stderr = stdout, stderr
	return runProcessTree(ctx, command)
}

type Manager struct {
	// Root is the install library directory, such as ~/.local/lib/pk.
	Root     string
	Runner   CommandRunner
	Progress func(stage string)
}

func (manager Manager) report(stage string) {
	if manager.Progress != nil {
		manager.Progress(stage)
	}
}

type Release struct {
	ID            string    `json:"id"`
	Path          string    `json:"path"`
	BinaryPath    string    `json:"binary_path"`
	UIPath        string    `json:"ui_path"`
	Source        string    `json:"source"`
	GitRepository string    `json:"git_repository,omitempty"`
	GitRef        string    `json:"git_ref,omitempty"`
	Revision      string    `json:"revision,omitempty"`
	Dirty         bool      `json:"dirty"`
	DirtyKnown    bool      `json:"dirty_known"`
	SourceHash    string    `json:"source_sha256"`
	StagedAt      time.Time `json:"staged_at"`
}

type Status struct {
	Current  *Release  `json:"current,omitempty"`
	Previous *Release  `json:"previous,omitempty"`
	Staged   []Release `json:"staged"`
}

// ImportArtifacts publishes an already-installed binary and matching UI tree
// as an immutable release. It is used once to migrate legacy installations.
func (manager Manager) ImportArtifacts(binaryPath, uiDirectory string) (Release, error) {
	return manager.importArtifacts(binaryPath, uiDirectory, nil)
}

// ImportArtifactsFromSource publishes prebuilt artifacts and records provenance
// from the source tree used to build them. It does not rebuild the source.
func (manager Manager) ImportArtifactsFromSource(ctx context.Context, binaryPath, uiDirectory, source string) (Release, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return Release{}, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return Release{}, fmt.Errorf("resolve install source: %w", err)
	}
	if err := validateSource(source); err != nil {
		return Release{}, err
	}
	revision, dirty, dirtyKnown := manager.sourceState(ctx, source)
	repository, ref := manager.sourceGitIdentity(ctx, source)
	hash, err := hashSource(source)
	if err != nil {
		return Release{}, fmt.Errorf("hash install source: %w", err)
	}
	provenance := &sourceProvenance{source: source, repository: repository, ref: ref, revision: revision, dirty: dirty, dirtyKnown: dirtyKnown, hash: hash}
	return manager.importArtifacts(binaryPath, uiDirectory, provenance)
}

type sourceProvenance struct {
	source, repository, ref, revision string
	dirty, dirtyKnown                 bool
	hash                              string
}

func (manager Manager) importArtifacts(binaryPath, uiDirectory string, provenance *sourceProvenance) (Release, error) {
	root, err := manager.root()
	if err != nil {
		return Release{}, err
	}
	binaryPath, err = filepath.Abs(binaryPath)
	if err != nil {
		return Release{}, err
	}
	uiDirectory, err = filepath.Abs(uiDirectory)
	if err != nil {
		return Release{}, err
	}
	if info, err := os.Stat(binaryPath); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return Release{}, fmt.Errorf("legacy binary is missing or not executable: %s", binaryPath)
	}
	for _, path := range []string{filepath.Join(uiDirectory, "dist", "main.js"), filepath.Join(uiDirectory, "node_modules")} {
		info, err := os.Stat(path)
		if err != nil || (strings.HasSuffix(path, "node_modules") && !info.IsDir()) {
			return Release{}, fmt.Errorf("legacy UI asset is missing: %s", path)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0o755); err != nil {
		return Release{}, err
	}
	suffix, err := randomHex(4)
	if err != nil {
		return Release{}, err
	}
	stagedAt := time.Now().UTC()
	id := "legacy-" + stagedAt.Format("20060102T150405") + "-" + suffix
	stagePath := filepath.Join(root, "releases", ".stage-"+id)
	finalPath := filepath.Join(root, "releases", id)
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		return Release{}, err
	}
	defer removeTree(stagePath)
	if err := copyFile(binaryPath, filepath.Join(stagePath, "pk"), 0o555); err != nil {
		return Release{}, err
	}
	if err := copyPath(uiDirectory, filepath.Join(stagePath, "ui")); err != nil {
		return Release{}, err
	}
	release := Release{ID: id, Path: finalPath, BinaryPath: filepath.Join(finalPath, "pk"), UIPath: filepath.Join(finalPath, "ui", "dist", "main.js"), Source: "legacy installation", StagedAt: stagedAt}
	if provenance != nil {
		release.Source = provenance.source
		if provenance.repository != "" {
			release.Source = provenance.repository
		}
		release.GitRepository, release.GitRef = provenance.repository, provenance.ref
		release.Revision, release.Dirty, release.DirtyKnown, release.SourceHash = provenance.revision, provenance.dirty, provenance.dirtyKnown, provenance.hash
	}
	if err := writeManifest(stagePath, release); err != nil {
		return Release{}, err
	}
	if err := sealTree(stagePath); err != nil {
		return Release{}, err
	}
	if err := os.Rename(stagePath, finalPath); err != nil {
		return Release{}, err
	}
	return release, nil
}

// Stage validates and copies an explicitly selected pk source tree, runs the
// repository Go tests and build script in that copy, then publishes a paired,
// immutable binary/UI release under Root/releases/<id>.
func (manager Manager) Stage(ctx context.Context, source string) (Release, error) {
	return manager.stage(ctx, source, "", "", "")
}

// StageGit builds an isolated checkout and records its immutable source
// provenance in the release metadata.
func (manager Manager) StageGit(ctx context.Context, checkout GitCheckout) (Release, error) {
	if checkout.Directory == "" || checkout.Repository == "" || checkout.Ref == "" || !validCommit(checkout.Commit) {
		return Release{}, errors.New("complete Git checkout metadata is required")
	}
	return manager.stage(ctx, checkout.Directory, checkout.Repository, checkout.Ref, checkout.Commit)
}

func (manager Manager) stage(ctx context.Context, source, gitRepository, gitRef, gitCommit string) (Release, error) {
	manager.report("source_validate")
	root, err := manager.root()
	if err != nil {
		return Release{}, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return Release{}, fmt.Errorf("resolve update source: %w", err)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return Release{}, fmt.Errorf("resolve update source: %w", err)
	}
	if err := validateSource(source); err != nil {
		return Release{}, err
	}
	revision, dirty, dirtyKnown := manager.sourceState(ctx, source)
	if gitCommit != "" {
		revision, dirty, dirtyKnown = gitCommit, false, true
	}
	buildRoot, err := os.MkdirTemp("", "pk-update-build-")
	if err != nil {
		return Release{}, fmt.Errorf("create isolated build source: %w", err)
	}
	defer os.RemoveAll(buildRoot)
	buildSource := filepath.Join(buildRoot, "source")
	manager.report("copy")
	hash, err := copyAndHashSource(source, buildSource)
	if err != nil {
		return Release{}, fmt.Errorf("copy update source: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0o755); err != nil {
		return Release{}, fmt.Errorf("create release directory: %w", err)
	}
	manager.report("test")
	if err := manager.runCommand(ctx, buildSource, "go", "test", "./..."); err != nil {
		return Release{}, fmt.Errorf("go test ./... failed: %w", err)
	}
	uiSource := filepath.Join(buildSource, "ui")
	manager.report("dependencies")
	if err := manager.runCommand(ctx, uiSource, "bun", "install", "--frozen-lockfile"); err != nil {
		return Release{}, fmt.Errorf("bun install --frozen-lockfile failed: %w", err)
	}
	manager.report("ui_validate")
	if err := manager.runCommand(ctx, uiSource, "bun", "run", "check"); err != nil {
		return Release{}, fmt.Errorf("UI validation (bun run check) failed: %w", err)
	}
	if err := manager.runCommand(ctx, uiSource, "bun", "test"); err != nil {
		return Release{}, fmt.Errorf("UI validation (bun test) failed: %w", err)
	}
	manager.report("build")
	if err := manager.runCommand(ctx, buildSource, "sh", "scripts/build"); err != nil {
		return Release{}, fmt.Errorf("scripts/build failed: %w", err)
	}
	manager.report("ui_validate")
	for _, args := range [][]string{{"run", "check"}, {"test"}} {
		if err := manager.runCommand(ctx, filepath.Join(buildSource, "ui"), "bun", args...); err != nil {
			return Release{}, fmt.Errorf("UI validation failed: %w", err)
		}
	}
	manager.report("dependencies")
	if err := manager.runCommand(ctx, filepath.Join(buildSource, "ui"), "bun", "install", "--production", "--frozen-lockfile"); err != nil {
		return Release{}, fmt.Errorf("bun install --production --frozen-lockfile failed: %w", err)
	}

	idSuffix, err := randomHex(4)
	if err != nil {
		return Release{}, err
	}
	stagedAt := time.Now().UTC()
	id := stagedAt.Format("20060102T150405.000000000Z") + "-" + hash[:12] + "-" + idSuffix
	stagePath := filepath.Join(root, "releases", ".stage-"+id)
	finalPath := filepath.Join(root, "releases", id)
	manager.report("stage")
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		return Release{}, fmt.Errorf("create staged release: %w", err)
	}
	defer removeTree(stagePath)
	binaryPath := filepath.Join(buildSource, "bin", "pk")
	if info, err := os.Stat(binaryPath); err != nil || !info.Mode().IsRegular() {
		return Release{}, errors.New("build did not produce bin/pk")
	}
	uiPath := filepath.Join(buildSource, "ui")
	if info, err := os.Stat(filepath.Join(uiPath, "dist", "main.js")); err != nil || !info.Mode().IsRegular() {
		return Release{}, errors.New("build did not produce ui/dist/main.js")
	}
	if info, err := os.Stat(filepath.Join(uiPath, "node_modules")); err != nil || !info.IsDir() {
		return Release{}, errors.New("build did not produce UI runtime dependencies")
	}
	if err := copyFile(binaryPath, filepath.Join(stagePath, "pk"), 0o555); err != nil {
		return Release{}, fmt.Errorf("stage binary: %w", err)
	}
	stageUI := filepath.Join(stagePath, "ui")
	if err := os.Mkdir(stageUI, 0o755); err != nil {
		return Release{}, err
	}
	for _, name := range []string{"package.json", "bun.lock", "dist", "node_modules"} {
		if err := copyPath(filepath.Join(uiPath, name), filepath.Join(stageUI, name)); err != nil {
			return Release{}, fmt.Errorf("stage UI %s: %w", name, err)
		}
	}
	releaseSource := source
	if gitRepository != "" {
		releaseSource = gitRepository
	}
	release := Release{
		ID: id, Path: finalPath, BinaryPath: filepath.Join(finalPath, "pk"), UIPath: filepath.Join(finalPath, "ui", "dist", "main.js"),
		Source: releaseSource, GitRepository: gitRepository, GitRef: gitRef,
		Revision: revision, Dirty: dirty, DirtyKnown: dirtyKnown, SourceHash: hash, StagedAt: stagedAt,
	}
	if err := writeManifest(stagePath, release); err != nil {
		return Release{}, err
	}
	if err := sealTree(stagePath); err != nil {
		return Release{}, err
	}
	if err := os.Rename(stagePath, finalPath); err != nil {
		return Release{}, fmt.Errorf("publish staged release: %w", err)
	}
	return release, nil
}

func (manager Manager) runCommand(ctx context.Context, directory, name string, args ...string) error {
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = 32<<10, 32<<10
	err := manager.commandRunner().Run(ctx, directory, name, &stdout, &stderr, args...)
	if err == nil {
		return nil
	}
	details := strings.TrimSpace(strings.TrimSpace(stdout.String()) + "\n" + strings.TrimSpace(stderr.String()))
	if details != "" {
		return fmt.Errorf("%w: %s", err, details)
	}
	return err
}

// Activate atomically points Root/current at an immutable staged release. The
// optional health check runs against the new executable after switching; a
// failure restores the prior current pointer before returning.
func (manager Manager) Activate(ctx context.Context, id string, healthCheck func(context.Context, Release) error) error {
	manager.report("activate")
	root, err := manager.root()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	unlock, err := lockUpdate(filepath.Join(root, ".activate.lock"))
	if err != nil {
		return fmt.Errorf("lock release activation: %w", err)
	}
	defer unlock()
	return manager.activateLocked(ctx, root, id, healthCheck)
}

func (manager Manager) activateLocked(ctx context.Context, root, id string, healthCheck func(context.Context, Release) error) error {
	release, err := manager.release(id)
	if err != nil {
		return err
	}
	if err := validateRelease(release); err != nil {
		return err
	}
	currentPath := filepath.Join(root, "current")
	oldTarget, oldExists, err := readPointer(currentPath)
	if err != nil {
		return err
	}
	if oldExists {
		if _, _, err := manager.releaseFromPointer(currentPath); err != nil {
			return fmt.Errorf("current release pointer is invalid: %w", err)
		}
	}
	newTarget := filepath.Join("releases", id)
	if err := replaceSymlink(root, currentPath, newTarget); err != nil {
		return err
	}
	rollback := func(cause error) error {
		var rollbackErr error
		if oldExists {
			rollbackErr = replaceSymlink(root, currentPath, oldTarget)
		} else {
			rollbackErr = os.Remove(currentPath)
			if errors.Is(rollbackErr, os.ErrNotExist) {
				rollbackErr = nil
			}
		}
		if rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("restore previous current release: %w", rollbackErr))
		}
		return cause
	}
	if healthCheck == nil {
		healthCheck = manager.defaultHealthCheck
	}
	if err := healthCheck(ctx, release); err != nil {
		return rollback(fmt.Errorf("release health check failed: %w", err))
	}
	if oldExists {
		if err := replaceSymlink(root, filepath.Join(root, "previous"), oldTarget); err != nil {
			return rollback(fmt.Errorf("record previous release: %w", err))
		}
	}
	return nil
}

func (manager Manager) Rollback(ctx context.Context, healthCheck func(context.Context, Release) error) error {
	root, err := manager.root()
	if err != nil {
		return err
	}
	unlock, err := lockUpdate(filepath.Join(root, ".activate.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	previous, exists, err := manager.releaseFromPointer(filepath.Join(root, "previous"))
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("there is no previous release to restore")
	}
	return manager.activateLocked(ctx, root, previous.ID, healthCheck)
}

func (manager Manager) Status() (Status, error) {
	root, err := manager.root()
	if err != nil {
		return Status{}, err
	}
	status := Status{Staged: []Release{}}
	if release, exists, err := manager.releaseFromPointer(filepath.Join(root, "current")); err != nil {
		return Status{}, err
	} else if exists {
		status.Current = &release
	}
	if release, exists, err := manager.releaseFromPointer(filepath.Join(root, "previous")); err != nil {
		return Status{}, err
	} else if exists {
		status.Previous = &release
	}
	entries, err := os.ReadDir(filepath.Join(root, "releases"))
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return Status{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		release, err := manager.release(entry.Name())
		if err == nil {
			status.Staged = append(status.Staged, release)
		}
	}
	sort.Slice(status.Staged, func(i, j int) bool { return status.Staged[i].StagedAt.Before(status.Staged[j].StagedAt) })
	return status, nil
}

func (manager Manager) defaultHealthCheck(ctx context.Context, release Release) error {
	if err := validateRelease(release); err != nil {
		return err
	}
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = 16<<10, 16<<10
	if err := manager.commandRunner().Run(ctx, filepath.Dir(release.BinaryPath), release.BinaryPath, &stdout, &stderr, "--help"); err != nil {
		return commandError("release --help", err, stderr.String())
	}
	return nil
}

func (manager Manager) releaseFromPointer(path string) (Release, bool, error) {
	target, exists, err := readPointer(path)
	if err != nil || !exists {
		return Release{}, exists, err
	}
	cleanTarget := filepath.Clean(target)
	if filepath.IsAbs(cleanTarget) {
		return Release{}, false, fmt.Errorf("release pointer %s must be relative", path)
	}
	if filepath.Dir(cleanTarget) != "releases" {
		return Release{}, false, fmt.Errorf("release pointer %s escapes release directory", path)
	}
	release, err := manager.release(filepath.Base(cleanTarget))
	return release, true, err
}

func (manager Manager) release(id string) (Release, error) {
	if id == "" || filepath.Base(id) != id || strings.HasPrefix(id, ".") {
		return Release{}, fmt.Errorf("invalid release ID %q", id)
	}
	root, err := manager.root()
	if err != nil {
		return Release{}, err
	}
	path := filepath.Join(root, "releases", id)
	data, err := os.ReadFile(filepath.Join(path, "release.json"))
	if err != nil {
		return Release{}, fmt.Errorf("read release %q: %w", id, err)
	}
	var release Release
	if err := json.Unmarshal(data, &release); err != nil {
		return Release{}, fmt.Errorf("decode release %q: %w", id, err)
	}
	if release.ID != id || release.Path != path {
		return Release{}, fmt.Errorf("release %q metadata does not match its path", id)
	}
	return release, nil
}

func validateRelease(release Release) error {
	if info, err := os.Stat(release.BinaryPath); err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("release binary is missing or not executable: %s", release.BinaryPath)
	}
	if info, err := os.Stat(release.UIPath); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("release UI entry is missing: %s", release.UIPath)
	}
	if info, err := os.Stat(filepath.Join(release.Path, "ui", "node_modules")); err != nil || !info.IsDir() {
		return fmt.Errorf("release UI dependencies are missing: %s", filepath.Join(release.Path, "ui", "node_modules"))
	}
	return nil
}

func (manager Manager) root() (string, error) {
	if strings.TrimSpace(manager.Root) == "" {
		return "", errors.New("update root is required")
	}
	root, err := filepath.Abs(manager.Root)
	if err != nil {
		return "", err
	}
	return root, nil
}

func (manager Manager) commandRunner() CommandRunner {
	if manager.Runner != nil {
		return manager.Runner
	}
	return osCommandRunner{}
}

func (manager Manager) sourceState(ctx context.Context, source string) (revision string, dirty, known bool) {
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = 16<<10, 16<<10
	if manager.commandRunner().Run(ctx, source, "git", &stdout, &stderr, "rev-parse", "HEAD") == nil {
		revision = strings.TrimSpace(stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if manager.commandRunner().Run(ctx, source, "git", &stdout, &stderr, "status", "--porcelain=v1", "--untracked-files=all") == nil {
		known = true
		dirty = strings.TrimSpace(stdout.String()) != ""
	}
	return
}

func (manager Manager) sourceGitIdentity(ctx context.Context, source string) (repository, ref string) {
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = 16<<10, 16<<10
	if manager.commandRunner().Run(ctx, source, "git", &stdout, &stderr, "config", "--get", "remote.origin.url") == nil {
		repository = sanitizeRepositoryURL(strings.TrimSpace(stdout.String()))
	}
	stdout.Reset()
	stderr.Reset()
	if manager.commandRunner().Run(ctx, source, "git", &stdout, &stderr, "symbolic-ref", "--quiet", "--short", "HEAD") == nil {
		ref = strings.TrimSpace(stdout.String())
	}
	return repository, ref
}

func sanitizeRepositoryURL(repository string) string {
	if !strings.Contains(repository, "://") {
		// Git's scp-like SSH form (for example git@host:owner/repo.git) is
		// intentionally preserved; it has no URL query or fragment fields.
		return repository
	}
	parsed, err := url.Parse(repository)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func validateSource(source string) error {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("update source is not a directory: %s", source)
	}
	module, err := readModulePath(filepath.Join(source, "go.mod"))
	if err != nil {
		return err
	}
	if module != modulePath {
		return fmt.Errorf("update source module is %q; expected %q", module, modulePath)
	}
	for _, path := range []string{"cmd/pk/main.go", "scripts/build", "ui/package.json", "ui/bun.lock"} {
		if _, err := os.Stat(filepath.Join(source, path)); err != nil {
			return fmt.Errorf("update source is missing %s: %w", path, err)
		}
	}
	return nil
}

func readModulePath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open update source go.mod: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if value, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(value), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("update source go.mod has no module declaration")
}

func copyAndHashSource(source, destination string) (string, error) {
	hash := sha256.New()
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return "", err
	}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if excludedSourcePath(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolve source symlink %q: %w", rel, err)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return err
			}
			inside, err := filepath.Rel(source, resolved)
			if err != nil || filepath.IsAbs(inside) || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("source symlink %q points outside the selected source tree", rel)
			}
			_, _ = io.WriteString(hash, "symlink\x00"+filepath.ToSlash(rel)+"\x00"+link+"\x00")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			copyTarget := filepath.Join(destination, inside)
			copyLink, err := filepath.Rel(filepath.Dir(target), copyTarget)
			if err != nil {
				return err
			}
			return os.Symlink(copyLink, target)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hash, "file\x00"+filepath.ToSlash(rel)+"\x00")
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()|0o200)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(io.MultiWriter(hash, output), input)
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		if outputCloseErr != nil {
			return outputCloseErr
		}
		_, _ = io.WriteString(hash, "\x00")
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// hashSource uses the same deterministic source-file selection and hashing as
// Stage without building or copying artifacts into a release.
func hashSource(source string) (string, error) {
	stage, err := os.MkdirTemp("", "pk-source-hash-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	return copyAndHashSource(source, filepath.Join(stage, "source"))
}

func excludedSourcePath(rel string, isDir bool) bool {
	clean := filepath.Clean(rel)
	parts := strings.Split(clean, string(filepath.Separator))
	for _, part := range parts {
		if part == ".git" || part == "node_modules" {
			return true
		}
	}
	if len(parts) == 1 && parts[0] == "bin" {
		return true
	}
	if len(parts) == 2 && parts[0] == "ui" && parts[1] == "dist" {
		return true
	}
	return false
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func copyPath(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.Symlink(link, destination)
	}
	if info.IsDir() {
		if err := os.Mkdir(destination, info.Mode().Perm()|0o200); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported non-regular release asset %q", source)
	}
	return copyFile(source, destination, info.Mode().Perm()|0o200)
}

func writeManifest(directory string, release Release) error {
	data, err := json.MarshalIndent(release, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "release.json"), append(data, '\n'), 0o600)
}

func sealTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o444) | (info.Mode().Perm() & 0o111)
		if entry.IsDir() {
			mode = 0o555
		} else if filepath.Base(path) == "pk" {
			mode = 0o555
		}
		return os.Chmod(path, mode)
	})
}

func removeTree(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
	_ = os.RemoveAll(root)
}

func replaceSymlink(root, path, target string) error {
	nonce, err := randomHex(4)
	if err != nil {
		return err
	}
	temp := filepath.Join(root, ".pointer-"+nonce)
	if err := os.Symlink(target, temp); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("switch release pointer %s: %w", path, err)
	}
	return nil
}

func readPointer(path string) (string, bool, error) {
	target, err := os.Readlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read release pointer %s: %w", path, err)
	}
	return target, true, nil
}

func randomHex(bytesNeeded int) (string, error) {
	data := make([]byte, bytesNeeded)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func commandError(label string, err error, details string) error {
	details = strings.TrimSpace(details)
	if details != "" {
		return fmt.Errorf("%s failed: %w: %s", label, err, details)
	}
	return fmt.Errorf("%s failed: %w", label, err)
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	originalLen := len(data)
	if buffer.limit > 0 && buffer.Len() < buffer.limit {
		remaining := buffer.limit - buffer.Len()
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.Buffer.Write(data)
	}
	return originalLen, nil
}
