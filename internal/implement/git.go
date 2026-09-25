package implement

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// prepare clones the remote's default branch into dir, which must not exist or
// be empty, and puts it on a new branch for the issue. It returns the branch
// and the commit the work starts from.
//
// The branch is `<prefix><n>-<k>`, where k is the first number with no branch
// of that name on the remote. A branch the agent has already pushed is never
// reused: the push that made it is on the remote, so its k is taken, and the
// work goes on a new one instead of over it (dotfiles ADR 0007 §4).
//
// The clone sends no credentials. Pushing is the only thing that needs them,
// and it is not done here.
func prepare(ctx context.Context, remote, dir, prefix string, issue int) (branch, base string, err error) {
	if _, err := git(ctx, "", "clone", "--quiet", "--no-tags", remote, dir); err != nil {
		return "", "", err
	}
	heads, err := git(ctx, dir, "ls-remote", "--heads", "origin")
	if err != nil {
		return "", "", err
	}
	taken := map[string]bool{}
	for _, line := range strings.Split(heads, "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			taken[strings.TrimPrefix(ref, "refs/heads/")] = true
		}
	}
	for k := 1; ; k++ {
		branch = prefix + strconv.Itoa(issue) + "-" + strconv.Itoa(k)
		if !taken[branch] {
			break
		}
	}
	if _, err := git(ctx, dir, "switch", "--quiet", "--create", branch); err != nil {
		return "", "", err
	}
	base, err = git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return branch, base, nil
}

// commits is how many commits the workspace's branch has on top of base.
func commits(ctx context.Context, dir, base string) (int, error) {
	out, err := git(ctx, dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// uncommitted is what `git status` says is not committed in the workspace:
// empty for a clean tree.
func uncommitted(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "status", "--porcelain")
}

// branchOf is the branch the workspace has checked out.
func branchOf(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "symbolic-ref", "--short", "HEAD")
}

// git runs one git command in dir, or in the process's own directory if dir is
// empty, and returns its output trimmed.
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
