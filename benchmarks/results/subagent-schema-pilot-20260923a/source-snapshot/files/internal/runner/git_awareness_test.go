package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProbeWorkspaceGitTriState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	nonrepo := filepath.Join(root, "nonrepo")
	if err := os.MkdirAll(nonrepo, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := probeWorkspaceGit(context.Background(), nonrepo); got != workspaceGitNotWorkTree {
		t.Fatalf("non-repository state=%v, want not-worktree", got)
	}

	repository := filepath.Join(root, "repository")
	runTestGit(t, "init", "-q", repository)
	nested := filepath.Join(repository, "nested", "dir")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := probeWorkspaceGit(context.Background(), nested); got != workspaceGitWorkTree {
		t.Fatalf("nested worktree state=%v, want worktree", got)
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, "-C", repository, "config", "user.email", "fixture@example.invalid")
	runTestGit(t, "-C", repository, "config", "user.name", "fixture")
	runTestGit(t, "-C", repository, "add", "README.md")
	runTestGit(t, "-C", repository, "commit", "-qm", "fixture")
	linkedWorktree := filepath.Join(root, "linked-worktree")
	runTestGit(t, "-C", repository, "worktree", "add", "--detach", "-q", linkedWorktree, "HEAD")
	if got := probeWorkspaceGit(context.Background(), linkedWorktree); got != workspaceGitWorkTree {
		t.Fatalf("linked worktree state=%v, want worktree", got)
	}

	bare := filepath.Join(root, "bare.git")
	runTestGit(t, "init", "--bare", "-q", bare)
	if got := probeWorkspaceGit(context.Background(), bare); got != workspaceGitNotWorkTree {
		t.Fatalf("bare repository state=%v, want not-worktree", got)
	}

	malformed := filepath.Join(root, "malformed")
	if err := os.MkdirAll(malformed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, ".git"), []byte("gitdir: missing target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := probeWorkspaceGit(context.Background(), malformed); got != workspaceGitUnknown {
		t.Fatalf("malformed repository state=%v, want unknown", got)
	}
}

func TestProbeWorkspaceGitIgnoresInheritedDiscoveryAndHandlesMissingGit(t *testing.T) {
	root := t.TempDir()
	nonrepo := filepath.Join(root, "workspace")
	if err := os.MkdirAll(nonrepo, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "other-repo")
	runTestGit(t, "init", "-q", repository)
	t.Setenv("GIT_DIR", filepath.Join(repository, ".git"))
	t.Setenv("GIT_WORK_TREE", repository)
	if got := probeWorkspaceGit(context.Background(), nonrepo); got != workspaceGitNotWorkTree {
		t.Fatalf("inherited Git discovery changed workspace state to %v", got)
	}

	if runtime.GOOS == "windows" {
		t.Skip("empty PATH executable lookup is platform-specific")
	}
	t.Setenv("PATH", t.TempDir())
	if got := probeWorkspaceGit(context.Background(), nonrepo); got != workspaceGitUnknown {
		t.Fatalf("missing git state=%v, want unknown", got)
	}
}

func TestProbeWorkspaceGitTimeoutIsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake git executable uses a POSIX script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	started := time.Now()
	if got := probeWorkspaceGit(context.Background(), t.TempDir()); got != workspaceGitUnknown {
		t.Fatalf("timed-out git state=%v, want unknown", got)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("500ms probe exceeded bounded return time: %s", elapsed)
	}
}

func runTestGit(t *testing.T, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Env = gitProbeEnvironment(os.Environ())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func BenchmarkProbeWorkspaceGitNonRepository(b *testing.B) {
	workspace := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if state := probeWorkspaceGit(context.Background(), workspace); state != workspaceGitNotWorkTree {
			b.Fatalf("state=%v, want not-worktree", state)
		}
	}
}
