package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pkupdate "github.com/pkyanam/pk/internal/update"
)

func TestInstallArtifactsMigratesLegacyPairAndWritesLauncher(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PK_HOME", filepath.Join(home, ".pk"))
	binDir, libDir := filepath.Join(home, "bin with spaces"), filepath.Join(home, "lib with spaces")
	t.Cleanup(func() {
		_ = filepath.WalkDir(libDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
			return nil
		})
	})
	t.Setenv("PK_BIN_DIR", binDir)
	t.Setenv("PK_LIB_DIR", libDir)
	legacyBin := filepath.Join(home, "old", "pk")
	legacyUI := filepath.Join(home, "old", "ui")
	newBin := filepath.Join(home, "build", "pk")
	newUI := filepath.Join(home, "build", "ui")
	for _, binary := range []string{legacyBin, newBin} {
		if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, ui := range []string{legacyUI, newUI} {
		if err := os.MkdirAll(filepath.Join(ui, "dist"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(ui, "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ui, "dist", "main.js"), []byte("// ui"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code := runInstallArtifacts(context.Background(), []string{"--binary", newBin, "--ui", newUI, "--legacy-binary", legacyBin, "--legacy-ui", legacyUI}, &strings.Builder{}, &strings.Builder{})
	if code != 0 {
		t.Fatalf("runInstallArtifacts returned %d", code)
	}
	status, err := updateManager().Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Current == nil || status.Previous == nil {
		t.Fatalf("expected active and rollback releases, got %#v", status)
	}
	if got, err := os.Readlink(filepath.Join(libDir, "current")); err != nil || got != filepath.Join("releases", status.Current.ID) {
		t.Fatalf("current link = %q, %v", got, err)
	}
	launcher, err := os.ReadFile(filepath.Join(binDir, "pk"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(launcher), "PK_UI_ENTRY=\"$PK_RELEASE_DIR/ui/dist/main.js\"") {
		t.Fatalf("launcher did not quote paired UI path: %s", launcher)
	}
	newID, legacyID := status.Current.ID, status.Previous.ID
	if err := updateManager().Rollback(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	status, err = updateManager().Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Current.ID != legacyID || status.Previous.ID != newID {
		t.Fatalf("rollback pointers did not toggle: current=%s previous=%s", status.Current.ID, status.Previous.ID)
	}
}

func TestInstallArtifactsRejectsTemporaryLibraryWithDurableDefaultLauncher(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PK_HOME", filepath.Join(home, ".pk"))
	libDir := filepath.Join(t.TempDir(), "pk-inst-check", "lib")
	t.Setenv("PK_LIB_DIR", libDir)
	t.Setenv("PK_BIN_DIR", "")
	bin := filepath.Join(home, "build", "pk")
	ui := filepath.Join(home, "build", "ui")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ui, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ui, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ui, "dist", "main.js"), []byte("// ui"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runInstallArtifacts(context.Background(), []string{"--binary", bin, "--ui", ui}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "PK_LIB_DIR points into the temporary directory") {
		t.Fatalf("runInstallArtifacts code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "pk")); !os.IsNotExist(err) {
		t.Fatalf("durable launcher was unexpectedly created: %v", err)
	}
	if _, err := os.Stat(libDir); !os.IsNotExist(err) {
		t.Fatalf("temporary release tree was unexpectedly created: %v", err)
	}
}

func TestValidateInstallDirsAllowsExplicitTemporaryPair(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PK_LIB_DIR", filepath.Join(root, "lib"))
	t.Setenv("PK_BIN_DIR", filepath.Join(root, "bin"))
	if err := validateInstallDirs(); err != nil {
		t.Fatalf("explicit temporary install pair rejected: %v", err)
	}
}

func TestMainDispatchesInstallReleaseHelper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr strings.Builder
	code := runMain([]string{"__install-release"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "usage: pk __install-release") {
		t.Fatalf("runMain __install-release code=%d stderr=%q; helper command was not dispatched", code, stderr.String())
	}
}

type githubUpdateRunner struct{ checkout string }

func (runner *githubUpdateRunner) Run(ctx context.Context, dir, name string, stdout, stderr io.Writer, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "git" && len(args) > 0 {
		switch args[0] {
		case "clone":
			destination := args[len(args)-1]
			runner.checkout = destination
			files := map[string]string{
				"go.mod":          "module github.com/pkyanam/pk\n\ngo 1.27.0\n",
				"cmd/pk/main.go":  "package main\nfunc main() {}\n",
				"scripts/build":   "#!/bin/sh\nexit 0\n",
				"ui/package.json": "{}\n",
				"ui/bun.lock":     "fixture\n",
			}
			for rel, body := range files {
				path := filepath.Join(destination, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					return err
				}
			}
		case "rev-parse":
			_, _ = io.WriteString(stdout, "abcdef0123456789abcdef0123456789abcdef01\n")
		}
		return nil
	}
	if name == "sh" && len(args) == 1 && args[0] == "scripts/build" {
		for _, dir := range []string{filepath.Join(dir, "bin"), filepath.Join(dir, "ui", "dist"), filepath.Join(dir, "ui", "node_modules")} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "bin", "pk"), []byte("fixture"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "ui", "dist", "main.js"), []byte("// fixture"), 0o644)
	}
	return nil
}

func TestDefaultUpdateSourceStagesOfficialGitHubMainAndRecordsCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lib", "pk")
	runner := &githubUpdateRunner{}
	var stages []string
	manager := pkupdate.Manager{Root: root, Runner: runner, Progress: func(stage string) { stages = append(stages, stage) }}
	t.Cleanup(func() { makeUpdateTreeWritable(root) })
	release, err := stageUpdateSource(context.Background(), manager, "")
	if err != nil {
		t.Fatal(err)
	}
	if release.GitRepository != pkupdate.DefaultGitRepository || release.GitRef != pkupdate.DefaultGitRef || release.Revision != "abcdef0123456789abcdef0123456789abcdef01" || release.Dirty {
		t.Fatalf("release provenance=%+v", release)
	}
	if runner.checkout == "" {
		t.Fatal("default update did not clone a checkout")
	}
	if len(stages) < 3 || stages[0] != "clone" || stages[1] != "resolve_revision" || stages[len(stages)-1] != "stage" {
		t.Fatalf("fake update stage notifications=%v", stages)
	}
	if _, err := os.Stat(runner.checkout); !os.IsNotExist(err) {
		t.Fatalf("temporary checkout still exists after stage: %v", err)
	}
}

func TestCLIUpdateProgressUsesReadableLabelsOnStderr(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "invalid-source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PK_LIB_DIR", filepath.Join(root, "lib"))
	t.Setenv("PK_BIN_DIR", filepath.Join(root, "bin"))
	var stdout, stderr strings.Builder
	code := runUpdate(context.Background(), []string{"--source", source}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runUpdate code=%d, want 1; stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("failure wrote to stdout: %s", stdout.String())
	}
	got := stderr.String()
	for _, want := range []string{"Validating the selected pk source", "pk update: stage failed:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output missing %q: %s", want, got)
		}
	}
}

func makeUpdateTreeWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		} else {
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/tmp/a'b"); got != "'/tmp/a'\\''b'" {
		t.Fatalf("shellQuote = %q", got)
	}
}

func TestLauncherPinsUIToExecutingReleaseDuringActivation(t *testing.T) {
	root := t.TempDir()
	binDir, libDir := filepath.Join(root, "bin"), filepath.Join(root, "lib")
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(libDir, "releases", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nln -s releases/second \"$PK_LIB_DIR/next\"\nmv -f \"$PK_LIB_DIR/next\" \"$PK_LIB_DIR/current\"\nprintf '%s' \"$PK_UI_ENTRY\"\n"
		if err := os.WriteFile(filepath.Join(dir, "pk"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("releases/first", filepath.Join(libDir, "current")); err != nil {
		t.Fatal(err)
	}
	if err := installStableLauncher(binDir, libDir); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(binDir, "pk")).Output()
	if err != nil {
		t.Fatal(err)
	}
	// macOS may canonicalize /var to /private/var in pwd -P.
	realLib, err := filepath.EvalSymlinks(libDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realLib, "releases", "first", "ui", "dist", "main.js")
	if string(out) != want {
		t.Fatalf("running release UI changed during activation: %s want %s", out, want)
	}
}

func TestChooseLegacyBinarySkipsStableShellLauncher(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, "launcher")
	actual := filepath.Join(root, "actual-pk")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec pk-current\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actual, []byte("MZ\x00pk-binary-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := chooseLegacyBinary(launcher, actual); got != actual {
		t.Fatalf("legacy binary=%q want %q", got, actual)
	}
}

func TestChooseLegacyBinaryPrefersInstalledBinary(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "installed")
	running := filepath.Join(root, "running")
	for _, path := range []string{installed, running} {
		if err := os.WriteFile(path, []byte("MZ fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := chooseLegacyBinary(installed, running); got != installed {
		t.Fatalf("legacy binary=%q want installed %q", got, installed)
	}
}

func TestUpdateAliasesReachUpdateCommand(t *testing.T) {
	for _, alias := range []string{"--update", "-update"} {
		var stdout, stderr strings.Builder
		if code := runMain([]string{alias, "--help"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("%s --help returned %d: %s", alias, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "Usage of update") {
			t.Fatalf("%s did not dispatch to update flags: stdout=%q stderr=%q", alias, stdout.String(), stderr.String())
		}
	}
}
