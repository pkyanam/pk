package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	pkupdate "github.com/pkyanam/pk/internal/update"
)

func updateManager() pkupdate.Manager { return pkupdate.Manager{Root: updateLibraryDir()} }

func updateManagerWithProgress(progress func(string)) pkupdate.Manager {
	return pkupdate.Manager{Root: updateLibraryDir(), Progress: progress}
}

func updateLibraryDir() string {
	if value := strings.TrimSpace(os.Getenv("PK_LIB_DIR")); value != "" {
		return value
	}
	return filepath.Join(userHome(), ".local", "lib", "pk")
}

func updateBinaryDir() string {
	if value := strings.TrimSpace(os.Getenv("PK_BIN_DIR")); value != "" {
		return value
	}
	return filepath.Join(userHome(), ".local", "bin")
}

func runUpdateCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: pk update [--source DIR]")
		return 2
	}
	switch args[0] {
	case "__install-artifacts":
		return runInstallArtifacts(ctx, args[1:], stdout, stderr)
	case "update":
		return runUpdate(ctx, args[1:], stdout, stderr)
	case "rollback":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: pk rollback")
			return 2
		}
		manager := updateManager()
		if err := manager.Rollback(ctx, nil); err != nil {
			fmt.Fprintf(stderr, "pk rollback: %v\n", err)
			return 1
		}
		status, err := manager.Status()
		if err != nil {
			fmt.Fprintf(stderr, "pk rollback: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Restored pk release %s.\n", status.Current.ID)
		return 0
	case "version":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: pk version")
			return 2
		}
		status, err := updateManager().Status()
		if err != nil {
			fmt.Fprintf(stderr, "pk version: %v\n", err)
			return 1
		}
		if status.Current == nil {
			fmt.Fprintln(stdout, "pk development build (no managed release)")
			return 0
		}
		fmt.Fprintf(stdout, "pk release %s\nsource: %s\nrevision: %s\nsource sha256: %s\n", status.Current.ID, status.Current.Source, status.Current.Revision, status.Current.SourceHash)
		if status.Current.DirtyKnown {
			if status.Current.Dirty {
				fmt.Fprintln(stdout, "source checkout: dirty")
			} else {
				fmt.Fprintln(stdout, "source checkout: clean")
			}
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: pk update [--source DIR] | pk rollback | pk version")
		return 2
	}
}

func runInstallArtifacts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("install-artifacts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	binary := flags.String("binary", "", "built pk binary")
	ui := flags.String("ui", "", "built UI directory")
	legacyBinary := flags.String("legacy-binary", "", "currently installed binary to preserve")
	legacyUI := flags.String("legacy-ui", "", "currently installed UI directory to preserve")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *binary == "" || *ui == "" {
		fmt.Fprintln(stderr, "usage: pk __install-artifacts --binary FILE --ui DIR [--legacy-binary FILE --legacy-ui DIR]")
		return 2
	}
	manager := updateManager()
	status, err := manager.Status()
	if err != nil {
		fmt.Fprintf(stderr, "pk install: %v\n", err)
		return 1
	}
	if status.Current == nil && *legacyBinary != "" && *legacyUI != "" {
		legacy, err := manager.ImportArtifacts(*legacyBinary, *legacyUI)
		if err != nil {
			fmt.Fprintf(stderr, "pk install: preserve legacy install: %v\n", err)
			return 1
		}
		if err := manager.Activate(ctx, legacy.ID, nil); err != nil {
			fmt.Fprintf(stderr, "pk install: activate legacy install: %v\n", err)
			return 1
		}
	}
	release, err := manager.ImportArtifacts(*binary, *ui)
	if err != nil {
		fmt.Fprintf(stderr, "pk install: stage artifacts: %v\n", err)
		return 1
	}
	if err := manager.Activate(ctx, release.ID, nil); err != nil {
		fmt.Fprintf(stderr, "pk install: activate release: %v\n", err)
		return 1
	}
	if err := installStableLauncher(updateBinaryDir(), updateLibraryDir()); err != nil {
		fmt.Fprintf(stderr, "pk install: write launcher: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Installed pk release %s at %s.\n", release.ID, release.Path)
	return 0
}

func runUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runUpdateWithProgress(ctx, args, stdout, stderr, nil)
}

func runUpdateWithProgress(ctx context.Context, args []string, stdout, stderr io.Writer, progress func(string)) int {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "", "pk source checkout (defaults to current directory if it is pk)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "pk update: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	selected := strings.TrimSpace(*source)
	if selected == "" {
		selected, _ = os.Getwd()
	}
	manager := updateManagerWithProgress(progress)
	release, err := manager.Stage(ctx, selected)
	if err != nil {
		fmt.Fprintf(stderr, "pk update: stage failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Staged %s from %s (source sha256 %s).\n", release.ID, release.Source, release.SourceHash)
	status, err := manager.Status()
	if err != nil {
		fmt.Fprintf(stderr, "pk update: %v\n", err)
		return 1
	}
	if status.Current == nil {
		legacy, err := manager.ImportArtifacts(currentExecutable(), filepath.Join(updateLibraryDir(), "ui"))
		if err != nil {
			fmt.Fprintf(stderr, "pk update: cannot preserve existing installation for rollback: %v\n", err)
			return 1
		}
		if err := manager.Activate(ctx, legacy.ID, nil); err != nil {
			fmt.Fprintf(stderr, "pk update: cannot activate preserved installation: %v\n", err)
			return 1
		}
		if err := installStableLauncher(updateBinaryDir(), updateLibraryDir()); err != nil {
			fmt.Fprintf(stderr, "pk update: cannot install stable launcher: %v\n", err)
			return 1
		}
	}
	if err := manager.Activate(ctx, release.ID, nil); err != nil {
		fmt.Fprintf(stderr, "pk update: activation failed: %v\n", err)
		return 1
	}
	if err := installStableLauncher(updateBinaryDir(), updateLibraryDir()); err != nil {
		fmt.Fprintf(stderr, "pk update: release activated, but launcher update failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Activated pk release %s. Previous release is available with `pk rollback`.\n", release.ID)
	return 0
}

func currentExecutable() string {
	path, err := os.Executable()
	if err == nil {
		return path
	}
	return filepath.Join(updateBinaryDir(), "pk")
}

func installStableLauncher(binaryDir, libDir string) error {
	var err error
	binaryDir, err = filepath.Abs(binaryDir)
	if err != nil {
		return err
	}
	libDir, err = filepath.Abs(libDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(binaryDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return err
	}
	launcher := filepath.Join(binaryDir, "pk")
	contents := "#!/bin/sh\nset -eu\nPK_BIN_DIR=" + shellQuote(binaryDir) + "\nexport PK_BIN_DIR\nPK_LIB_DIR=" + shellQuote(libDir) + "\nexport PK_LIB_DIR\nPK_RELEASE_DIR=$(CDPATH= cd -- \"$PK_LIB_DIR/current\" && pwd -P)\nexport PK_RELEASE_DIR\nPK_UI_ENTRY=\"$PK_RELEASE_DIR/ui/dist/main.js\"\nexport PK_UI_ENTRY\nexec \"$PK_RELEASE_DIR/pk\" \"$@\"\n"
	tmp, err := os.CreateTemp(binaryDir, ".pk-launcher-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.WriteString(tmp, contents); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, launcher)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func checkReleaseExecutable(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, path, "--help")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
