package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
