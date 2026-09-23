package update

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cloneCommandRunner struct {
	commands []string
	fail     bool
}

func (runner *cloneCommandRunner) Run(ctx context.Context, directory, name string, stdout, stderr io.Writer, args ...string) error {
	runner.commands = append(runner.commands, name+" "+strings.Join(args, " "))
	if err := ctx.Err(); err != nil {
		return err
	}
	if runner.fail {
		_, _ = io.WriteString(stderr, "scripted git failure")
		return os.ErrPermission
	}
	if name != "git" {
		return nil
	}
	switch args[0] {
	case "clone":
		destination := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(destination, "cmd", "pk"), 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(destination, "scripts"), 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(destination, "ui"), 0o755); err != nil {
			return err
		}
		files := map[string]string{
			"go.mod":          "module github.com/pkyanam/pk\n\ngo 1.27.0\n",
			"cmd/pk/main.go":  "package main\nfunc main() {}\n",
			"scripts/build":   "#!/bin/sh\nexit 0\n",
			"ui/package.json": "{}\n",
			"ui/bun.lock":     "fixture\n",
		}
		for name, body := range files {
			path := filepath.Join(destination, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				return err
			}
		}
	case "rev-parse":
		_, _ = io.WriteString(stdout, "0123456789abcdef0123456789abcdef01234567\n")
	}
	return nil
}

func TestCheckoutGitUsesPrivateShallowCloneAndCleansOnlyItsCheckout(t *testing.T) {
	runner := &cloneCommandRunner{}
	manager := Manager{Root: filepath.Join(t.TempDir(), "releases"), Runner: runner}
	checkout, err := manager.CheckoutGit(context.Background(), DefaultGitRepository, DefaultGitRef)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Repository != DefaultGitRepository || checkout.Ref != DefaultGitRef || checkout.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("checkout provenance=%+v", checkout)
	}
	if len(runner.commands) != 2 || !strings.Contains(runner.commands[0], "clone --depth=1 --single-branch --branch main -- https://github.com/pkyanam/pk.git") {
		t.Fatalf("git commands=%v", runner.commands)
	}
	if _, err := os.Stat(checkout.Directory); err != nil {
		t.Fatalf("checkout directory missing before close: %v", err)
	}
	if err := checkout.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(checkout.Directory); !os.IsNotExist(err) {
		t.Fatalf("owned checkout remained after close: %v", err)
	}
}

func TestCheckoutGitRejectsInsecureRepositoryAndCleansFailedClone(t *testing.T) {
	manager := Manager{Runner: &cloneCommandRunner{fail: true}}
	if _, err := manager.CheckoutGit(context.Background(), "http://example.com/pk.git", "main"); err == nil {
		t.Fatal("insecure repository accepted")
	}
	checkout, err := manager.CheckoutGit(context.Background(), DefaultGitRepository, "main")
	if err == nil {
		_ = checkout.Close()
		t.Fatal("failed clone succeeded")
	}
}

func TestStageGitPersistsRepositoryRefAndCommit(t *testing.T) {
	checkoutRunner := &cloneCommandRunner{}
	checkout, err := (Manager{Runner: checkoutRunner}).CheckoutGit(context.Background(), DefaultGitRepository, DefaultGitRef)
	if err != nil {
		t.Fatal(err)
	}
	defer checkout.Close()
	manager := Manager{Root: filepath.Join(t.TempDir(), "lib", "pk"), Runner: &fakeCommandRunner{revision: "different", dirtyStatus: " M file"}}
	t.Cleanup(func() { makeTreeWritable(t, manager.Root) })
	release, err := manager.StageGit(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	if release.Source != DefaultGitRepository || release.GitRepository != DefaultGitRepository || release.GitRef != "main" || release.Revision != checkout.Commit || release.Dirty || !release.DirtyKnown {
		t.Fatalf("release provenance=%+v", release)
	}
}
