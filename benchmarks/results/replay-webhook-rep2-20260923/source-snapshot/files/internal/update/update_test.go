package update

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeCommandRunner struct {
	fail        string
	revision    string
	dirtyStatus string
	commands    []string
}

func (runner *fakeCommandRunner) Run(ctx context.Context, directory, name string, stdout, stderr io.Writer, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := name + " " + strings.Join(args, " ")
	runner.commands = append(runner.commands, command)
	if command == runner.fail {
		_, _ = io.WriteString(stderr, "scripted failure")
		return errors.New("scripted command failure")
	}
	if name == "git" && len(args) > 0 {
		switch args[0] {
		case "rev-parse":
			_, _ = io.WriteString(stdout, runner.revision+"\n")
		case "status":
			_, _ = io.WriteString(stdout, runner.dirtyStatus)
		}
		return nil
	}
	if name == "sh" && len(args) == 1 && args[0] == "scripts/build" {
		if err := os.MkdirAll(filepath.Join(directory, "bin"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, "bin", "pk"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			return err
		}
		ui := filepath.Join(directory, "ui")
		if err := os.MkdirAll(filepath.Join(ui, "dist"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(ui, "dist", "main.js"), []byte("// fake UI entry\n"), 0o600); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(ui, "node_modules", "@opentui", "core"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(ui, "node_modules", "@opentui", "core", "native.node"), []byte("fixture"), 0o600); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func makeSource(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	files := map[string]string{
		"go.mod":          "module github.com/pkyanam/pk\n\ngo 1.27.0\n",
		"cmd/pk/main.go":  "package main\nfunc main() {}\n",
		"scripts/build":   "#!/bin/sh\nexit 0\n",
		"ui/package.json": `{"name":"pk-tui","type":"module"}` + "\n",
		"ui/bun.lock":     "fixture lock\n",
	}
	for path, contents := range files {
		full := filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return source
}

func TestStageBuildsPairedReleaseFromReadOnlySourceCopy(t *testing.T) {
	source := makeSource(t)
	runner := &fakeCommandRunner{revision: "cafe123", dirtyStatus: " M cmd/pk/main.go\n?? new-file.go\n"}
	var progress []string
	manager := Manager{Root: filepath.Join(t.TempDir(), "install", "pk"), Runner: runner, Progress: func(stage string) { progress = append(progress, stage) }}
	t.Cleanup(func() { makeTreeWritable(t, manager.Root) })
	release, err := manager.Stage(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if release.ID == "" || release.Revision != "cafe123" || !release.Dirty || !release.DirtyKnown || len(release.SourceHash) != 64 {
		t.Fatalf("release source evidence=%+v", release)
	}
	for _, path := range []string{release.BinaryPath, release.UIPath, filepath.Join(release.Path, "ui", "node_modules", "@opentui", "core", "native.node")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("paired release asset %s missing: %v", path, err)
		}
	}
	if err := validateRelease(release); err != nil {
		t.Fatalf("release failed validation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "bin")); !os.IsNotExist(err) {
		t.Fatalf("build modified original source bin directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "ui", "dist")); !os.IsNotExist(err) {
		t.Fatalf("build modified original UI dist: %v", err)
	}
	if !containsCommand(runner.commands, "go test ./...") || !containsCommand(runner.commands, "sh scripts/build") || !containsCommand(runner.commands, "bun install --production --frozen-lockfile") {
		t.Fatalf("build pipeline did not run all steps: %v", runner.commands)
	}
	wantProgress := []string{"source_validate", "copy", "test", "dependencies", "ui_validate", "build", "dependencies", "stage"}
	if !reflect.DeepEqual(progress, wantProgress) {
		t.Fatalf("progress=%v want %v", progress, wantProgress)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Current != nil || status.Previous != nil || len(status.Staged) != 1 || status.Staged[0].ID != release.ID {
		t.Fatalf("status after staging=%+v", status)
	}
}

func TestBuildEnvironmentDropsInstalledLauncherAndSessionOverrides(t *testing.T) {
	input := []string{"PATH=/toolchain/bin", "HOME=/home/test", "GOPROXY=https://proxy.example", "PK_UI_ENTRY=/installed/ui/main.js", "PK_EXECUTABLE=/installed/pk", "PK_MODEL=gpt-6-luna", "PK_SESSION=session-1", "PK_RELOAD_TOKEN=private", "CODEX_HOME=/private/codex"}
	got := buildEnvironment(input)
	want := []string{"PATH=/toolchain/bin", "HOME=/home/test", "GOPROXY=https://proxy.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildEnvironment()=%v, want %v", got, want)
	}
}

func TestActivateRollsBackOnHealthFailureAndManualRollbackRestoresPrevious(t *testing.T) {
	source := makeSource(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "updates"), Runner: &fakeCommandRunner{revision: "rev"}}
	t.Cleanup(func() { makeTreeWritable(t, manager.Root) })
	first, err := manager.Stage(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Activate(context.Background(), first.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "cmd/pk/main.go"), []byte("package main\n// second release\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Stage(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	wantFailure := errors.New("new process failed health check")
	if err := manager.Activate(context.Background(), second.ID, func(context.Context, Release) error { return wantFailure }); !errors.Is(err, wantFailure) {
		t.Fatalf("failed activation error=%v", err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Current == nil || status.Current.ID != first.ID || status.Previous != nil {
		t.Fatalf("failed activation did not restore current pointer: %+v", status)
	}
	if err := manager.Activate(context.Background(), second.ID, func(_ context.Context, release Release) error {
		if release.ID != second.ID {
			return errors.New("health check received wrong release")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Current == nil || status.Current.ID != second.ID || status.Previous == nil || status.Previous.ID != first.ID {
		t.Fatalf("successful activation pointers=%+v", status)
	}
	if err := manager.Rollback(context.Background(), func(context.Context, Release) error { return nil }); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Current == nil || status.Current.ID != first.ID || status.Previous == nil || status.Previous.ID != second.ID {
		t.Fatalf("manual rollback pointers=%+v", status)
	}
}

func TestStageStopsOnTestFailureWithoutPublishingRelease(t *testing.T) {
	source := makeSource(t)
	manager := Manager{Root: filepath.Join(t.TempDir(), "updates"), Runner: &fakeCommandRunner{fail: "go test ./..."}}
	t.Cleanup(func() { makeTreeWritable(t, manager.Root) })
	if _, err := manager.Stage(context.Background(), source); err == nil || !strings.Contains(err.Error(), "go test ./...") {
		t.Fatalf("stage failure=%v", err)
	}
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Staged) != 0 {
		t.Fatalf("failed build published releases: %+v", status.Staged)
	}
}

func TestStageRejectsSourceSymlinkEscapingSelectedTree(t *testing.T) {
	source := makeSource(t)
	external := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(external, []byte("package outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "outside-link.go")); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Root: filepath.Join(t.TempDir(), "updates"), Runner: &fakeCommandRunner{}}
	if _, err := manager.Stage(context.Background(), source); err == nil || !strings.Contains(err.Error(), "outside the selected source tree") {
		t.Fatalf("outside source symlink error=%v", err)
	}
}

func containsCommand(commands []string, command string) bool {
	for _, item := range commands {
		if item == command {
			return true
		}
	}
	return false
}

func makeTreeWritable(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0o755)
		if !entry.IsDir() {
			mode = 0o644
		}
		_ = os.Chmod(path, mode)
		return nil
	})
}

func TestStageRejectsFailingUIValidation(t *testing.T) {
	for _, step := range []string{"bun run check", "bun test"} {
		t.Run(step, func(t *testing.T) {
			manager := Manager{Root: filepath.Join(t.TempDir(), "install"), Runner: &fakeCommandRunner{fail: step}}
			if _, err := manager.Stage(context.Background(), makeSource(t)); err == nil || !strings.Contains(err.Error(), "UI validation") {
				t.Fatalf("expected UI gate error, got %v", err)
			}
			status, err := manager.Status()
			if err != nil || len(status.Staged) != 0 || status.Current != nil {
				t.Fatalf("failed UI was published: %+v %v", status, err)
			}
		})
	}
}

func TestSealedReleasePreservesExecutableDependencies(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { makeTreeWritable(t, root) })
	helper := filepath.Join(root, "native-helper")
	asset := filepath.Join(root, "asset.js")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("// asset"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := sealTree(root); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{helper: 0555, asset: 0444} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o want %o", path, info.Mode().Perm(), want)
		}
	}
}
