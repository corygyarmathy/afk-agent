package review

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Git checks a pull request's head out with the git binary.
type Git struct {
	// Remote is the repository to fetch from: the tracker's clone URL, or a
	// local path in a test.
	Remote string
}

// Checkout fetches refs/pull/<n>/head into dir and checks it out detached. The
// commit it returns is the one actually checked out, which is the head the
// review is of - the pull request may have moved since anyone last read it.
//
// Shallow, because a review reads a head and not its history. The diff against
// the base comes from the tracker rather than from git, which is what lets the
// fetch stay shallow.
func (g Git) Checkout(ctx context.Context, dir string, number int) (string, error) {
	steps := [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--depth=1", g.Remote, fmt.Sprintf("refs/pull/%d/head", number)},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := git(ctx, dir, args...); err != nil {
			return "", err
		}
	}
	return git(ctx, dir, "rev-parse", "HEAD")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Nothing on stdin and no prompt for credentials: an unattended fetch
	// that wants a password is a failure, not a wait.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
