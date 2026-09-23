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
	"runtime"
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
	case "__install-release":
		return runInstallRelease(ctx, args[1:], stdout, stderr)
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
		fmt.Fprintf(stdout, "pk release %s\nsource: %s\nrevision: %s\n", status.Current.ID, status.Current.Source, status.Current.Revision)
		if status.Current.SourceHash != "" {
			fmt.Fprintf(stdout, "source sha256: %s\n", status.Current.SourceHash)
		}
		if status.Current.DistributionSHA256 != "" {
			fmt.Fprintf(stdout, "distribution sha256: %s\n", status.Current.DistributionSHA256)
		}
		if status.Current.SourceHash != "" && status.Current.DirtyKnown {
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

func runInstallRelease(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("install-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	archivePath := flags.String("archive", "", "verified paired release archive")
	tag := flags.String("tag", "", "release tag")
	checksum := flags.String("sha256", "", "expected archive SHA-256")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *archivePath == "" || *tag == "" || *checksum == "" {
		fmt.Fprintln(stderr, "usage: pk __install-release --archive FILE --tag TAG --sha256 HEX")
		return 2
	}
	info, err := os.Lstat(*archivePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 256<<20 {
		fmt.Fprintln(stderr, "pk install: release archive is missing or invalid")
		return 1
	}
	if _, err := exec.LookPath("bun"); err != nil {
		fmt.Fprintln(stderr, "pk install: Bun is required to run the OpenTUI interface; install Bun before activating this release")
		return 1
	}
	data, err := os.ReadFile(*archivePath)
	if err != nil {
		fmt.Fprintf(stderr, "pk install: read release archive: %v\n", err)
		return 1
	}
	manager := updateManager()
	release, err := manager.StageReleaseArchive(ctx, data, *tag, *checksum)
	if err != nil {
		fmt.Fprintf(stderr, "pk install: stage verified release: %v\n", err)
		return 1
	}
	status, err := manager.Status()
	if err != nil {
		fmt.Fprintf(stderr, "pk install: inspect existing release: %v\n", err)
		return 1
	}
	if status.Current == nil {
		legacyBinary, legacyUI := legacyInstalledBinary(), filepath.Join(updateLibraryDir(), "ui")
		if _, binaryErr := os.Stat(legacyBinary); binaryErr == nil {
			if _, uiErr := os.Stat(legacyUI); uiErr == nil {
				legacy, err := manager.ImportArtifacts(legacyBinary, legacyUI)
				if err != nil {
					fmt.Fprintf(stderr, "pk install: preserve existing release for rollback: %v\n", err)
					return 1
				}
				if err := manager.Activate(ctx, legacy.ID, nil); err != nil {
					fmt.Fprintf(stderr, "pk install: activate preserved release: %v\n", err)
					return 1
				}
			}
		}
	}
	if err := manager.Activate(ctx, release.ID, nil); err != nil {
		fmt.Fprintf(stderr, "pk install: activate release: %v\n", err)
		return 1
	}
	if err := installStableLauncher(updateBinaryDir(), updateLibraryDir()); err != nil {
		fmt.Fprintf(stderr, "pk install: write launcher: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Installed pk release %s.\n", release.GitRef)
	return 0
}

func runInstallArtifacts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("install-artifacts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	binary := flags.String("binary", "", "built pk binary")
	ui := flags.String("ui", "", "built UI directory")
	legacyBinary := flags.String("legacy-binary", "", "currently installed binary to preserve")
	legacyUI := flags.String("legacy-ui", "", "currently installed UI directory to preserve")
	source := flags.String("source", "", "source tree used to build the artifacts")
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
	var release pkupdate.Release
	if *source != "" {
		release, err = manager.ImportArtifactsFromSource(ctx, *binary, *ui, *source)
	} else {
		release, err = manager.ImportArtifacts(*binary, *ui)
	}
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
	lastStage := ""
	code := runUpdateWithProgress(ctx, args, stdout, stderr, func(stage string) {
		lastStage = stage
		fmt.Fprintln(stderr, updateStageMessage(stage))
	})
	if code != 0 && lastStage != "" {
		fmt.Fprintf(stderr, "pk update stopped after: %s\n", updateStageMessage(lastStage))
	}
	return code
}

func runUpdateWithProgress(ctx context.Context, args []string, stdout, stderr io.Writer, progress func(string)) int {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "", "explicit pk source directory (default: install latest compatible published binary release)")
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
	manager := updateManagerWithProgress(progress)
	var release pkupdate.Release
	var err error
	selected := strings.TrimSpace(*source)
	if selected == "" {
		var alreadyCurrent bool
		release, alreadyCurrent, err = manager.StageLatestPrebuilt(ctx, runtime.GOOS, runtime.GOARCH)
		if errors.Is(err, pkupdate.ErrNoPublishedRelease) || errors.Is(err, pkupdate.ErrNoPlatformRelease) {
			fmt.Fprintln(stderr, "No compatible published binary release is available; falling back to the verified source build.")
			if progress != nil {
				progress("source_fallback")
			}
			release, err = stageUpdateSource(ctx, manager, "")
		} else if err == nil && alreadyCurrent {
			fmt.Fprintf(stdout, "pk %s is already on the latest release.\n", release.GitRef)
			return 0
		}
	} else {
		release, err = stageUpdateSource(ctx, manager, selected)
	}
	if err != nil {
		fmt.Fprintf(stderr, "pk update: stage failed: %v\n", err)
		return 1
	}
	if release.DistributionSHA256 != "" {
		fmt.Fprintf(stdout, "Staged %s from %s (release archive sha256 %s).\n", release.ID, release.Source, release.DistributionSHA256)
	} else {
		fmt.Fprintf(stdout, "Staged %s from %s (source sha256 %s).\n", release.ID, release.Source, release.SourceHash)
	}
	status, err := manager.Status()
	if err != nil {
		fmt.Fprintf(stderr, "pk update: %v\n", err)
		return 1
	}
	if status.Current == nil {
		legacy, err := manager.ImportArtifacts(legacyInstalledBinary(), filepath.Join(updateLibraryDir(), "ui"))
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
	if _, err := exec.LookPath("bun"); err != nil {
		fmt.Fprintln(stderr, "pk update: Bun is required to run the OpenTUI interface; install Bun before activating the paired release")
		return 1
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

func stageUpdateSource(ctx context.Context, manager pkupdate.Manager, selected string) (pkupdate.Release, error) {
	selected = strings.TrimSpace(selected)
	if selected != "" {
		return manager.Stage(ctx, selected)
	}
	checkout, err := manager.CheckoutGit(ctx, pkupdate.DefaultGitRepository, pkupdate.DefaultGitRef)
	if err != nil {
		return pkupdate.Release{}, fmt.Errorf("fetch official source: %w", err)
	}
	defer checkout.Close()
	return manager.StageGit(ctx, checkout)
}

func currentExecutable() string {
	path, err := os.Executable()
	if err == nil {
		return path
	}
	return filepath.Join(updateBinaryDir(), "pk")
}

func legacyInstalledBinary() string {
	installed := filepath.Join(updateBinaryDir(), "pk")
	return chooseLegacyBinary(installed, currentExecutable())
}

func chooseLegacyBinary(installed, running string) string {
	for _, candidate := range []string{installed, running} {
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			continue
		}
		file, err := os.Open(candidate)
		if err != nil {
			continue
		}
		var prefix [2]byte
		_, readErr := file.Read(prefix[:])
		_ = file.Close()
		if readErr == nil && string(prefix[:]) == "#!" {
			continue // Never preserve the stable shell launcher as the old binary.
		}
		return candidate
	}
	return installed
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
