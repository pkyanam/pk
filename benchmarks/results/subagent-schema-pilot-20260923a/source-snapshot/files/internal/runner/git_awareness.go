package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

type workspaceGitState uint8

const (
	workspaceGitUnknown workspaceGitState = iota
	workspaceGitWorkTree
	workspaceGitNotWorkTree
)

const workspaceGitProbeTimeout = 500 * time.Millisecond

// probeWorkspaceGit returns a fact only when Git positively identifies a
// work tree or emits its canonical C-locale no-repository diagnostic. Other
// errors, including malformed .git data, missing Git, and timeouts, stay
// unknown so the prompt never asserts a false non-repository state.
func probeWorkspaceGit(parent context.Context, workspace string) workspaceGitState {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, workspaceGitProbeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", workspace, "rev-parse", "--is-inside-work-tree")
	command.WaitDelay = 50 * time.Millisecond
	command.Env = gitProbeEnvironment(os.Environ())
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err == nil {
		switch strings.TrimSpace(stdout.String()) {
		case "true":
			return workspaceGitWorkTree
		case "false":
			return workspaceGitNotWorkTree
		default:
			return workspaceGitUnknown
		}
	}
	if ctx.Err() != nil {
		return workspaceGitUnknown
	}
	if strings.TrimSpace(stderr.String()) == "fatal: not a git repository (or any of the parent directories): .git" {
		return workspaceGitNotWorkTree
	}
	return workspaceGitUnknown
}

func gitProbeEnvironment(environment []string) []string {
	clean := make([]string, 0, len(environment)+4)
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean, "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
}
