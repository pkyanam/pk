package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const DefaultGitRepository = "https://github.com/pkyanam/pk.git"
const DefaultGitRef = "main"

// GitCheckout is a shallow, isolated checkout owned by the update operation.
// Close removes only its temporary directory; it never touches a user checkout.
type GitCheckout struct {
	Directory  string
	Repository string
	Ref        string
	Commit     string
}

func (checkout GitCheckout) Close() error {
	if checkout.Directory == "" {
		return nil
	}
	return os.RemoveAll(checkout.Directory)
}

// CheckoutGit creates a shallow checkout of repository/ref in a fresh temporary
// directory and resolves the checked out commit. Callers must Close the result.
func (manager Manager) CheckoutGit(ctx context.Context, repository, ref string) (GitCheckout, error) {
	repository = strings.TrimSpace(repository)
	ref = strings.TrimSpace(ref)
	if repository == "" || ref == "" {
		return GitCheckout{}, errors.New("update repository and ref are required")
	}
	if !strings.HasPrefix(repository, "https://") {
		return GitCheckout{}, errors.New("update repository must use HTTPS")
	}
	dir, err := os.MkdirTemp("", "pk-update-git-")
	if err != nil {
		return GitCheckout{}, fmt.Errorf("create isolated Git checkout: %w", err)
	}
	cleanup := func(err error) (GitCheckout, error) {
		_ = os.RemoveAll(dir)
		return GitCheckout{}, err
	}
	// git clone requires the destination not to exist. Remove only the private
	// temporary directory created above, then let git create it.
	if err := os.Remove(dir); err != nil {
		return cleanup(fmt.Errorf("prepare Git checkout: %w", err))
	}
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = 16<<10, 16<<10
	manager.report("clone")
	if err := manager.commandRunner().Run(ctx, "", "git", &stdout, &stderr,
		"clone", "--depth=1", "--single-branch", "--branch", ref, "--", repository, dir,
	); err != nil {
		return cleanup(commandFailure("clone update repository", err, &stdout, &stderr))
	}
	if err := validateSource(dir); err != nil {
		return cleanup(fmt.Errorf("cloned repository is not a pk source: %w", err))
	}
	manager.report("resolve_revision")
	stdout.Reset()
	stderr.Reset()
	if err := manager.commandRunner().Run(ctx, dir, "git", &stdout, &stderr, "rev-parse", "HEAD"); err != nil {
		return cleanup(commandFailure("resolve checked out commit", err, &stdout, &stderr))
	}
	commit := strings.TrimSpace(stdout.String())
	if !validCommit(commit) {
		return cleanup(errors.New("git returned an invalid checked out commit"))
	}
	return GitCheckout{Directory: dir, Repository: repository, Ref: ref, Commit: commit}, nil
}

func validCommit(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	for _, char := range commit {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func commandFailure(action string, err error, stdout, stderr io.Reader) error {
	var out, diagnostic strings.Builder
	_, _ = io.Copy(&out, stdout)
	_, _ = io.Copy(&diagnostic, stderr)
	detail := strings.TrimSpace(strings.TrimSpace(out.String()) + "\n" + strings.TrimSpace(diagnostic.String()))
	if detail != "" {
		return fmt.Errorf("%s: %w: %s", action, err, detail)
	}
	return fmt.Errorf("%s: %w", action, err)
}
