package implement

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// prepare clones the remote's default branch into dir, which must not exist or
// be empty, and puts it on a new branch for the issue. It returns the branch,
// the commit the work starts from, and the default branch the pull request
// will be into.
//
// The branch is `<prefix><n>-<k>`, where k is the first number with no branch
// of that name on the remote. A branch the agent has already pushed is never
// reused: the push that made it is on the remote, so its k is taken, and the
// work goes on a new one instead of over it. A lease lets the agent rewrite
// a push it saw land in this run, not one a run before it made.
//
// The clone sends no credentials. Pushing is the only thing that needs them,
// and it is not done here.
func prepare(ctx context.Context, remote, dir, prefix string, issue int) (branch, base, into string, err error) {
	if _, err := git(ctx, "", "clone", "--quiet", "--no-tags", remote, dir); err != nil {
		return "", "", "", err
	}
	into, err = git(ctx, dir, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return "", "", "", err
	}
	into = strings.TrimPrefix(into, "origin/")
	heads, err := git(ctx, dir, "ls-remote", "--heads", "origin")
	if err != nil {
		return "", "", "", err
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
		return "", "", "", err
	}
	base, err = git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", "", err
	}
	return branch, base, into, nil
}

// relay copies the workspace's branch into relayDir, a bare repository only
// the agent writes, and returns the commit it copied. The push is made from
// there and never from the workspace.
//
// The workspace's .git is the model's to write, and git obeys it: a pre-push
// hook, a core.fsmonitor command, or a url.insteadOf that sends the push
// somewhere else would each run with - or send away - the token the push
// carries. A fetch from the workspace into a repository the agent made reads
// only its objects: upload-pack takes no hooks from the repository it serves.
func relay(ctx context.Context, workspace, relayDir, branch string) (string, error) {
	if !isDir(relayDir) {
		if _, err := git(ctx, "", "init", "--quiet", "--bare", relayDir); err != nil {
			return "", err
		}
	}
	ref := "refs/heads/" + branch
	if _, err := git(ctx, relayDir, "fetch", "--quiet", "--no-tags", "--force", workspace, "+"+ref+":"+ref); err != nil {
		return "", err
	}
	return git(ctx, relayDir, "rev-parse", ref)
}

// touched is every path a commit in base..head adds, changes or deletes,
// commit by commit rather than in the net diff: a file added and then removed
// again is still in the history the push sends. Renames are a deletion and an
// addition, so both of their names are here.
func touched(ctx context.Context, dir, base, head string) ([]string, error) {
	out, err := git(ctx, dir, "log", "--no-renames", "--name-only", "--format=", "-z", base+".."+head)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// push sends head to the remote's branch from the relay, leased on lease: the
// commit the agent last saw its own push land at, or empty for a branch that
// must not exist yet (ADR 0001, the amendment of 2026-09-25).
//
// A session may amend or rebase commits the agent already pushed, and a plain
// push would then be refused as not a fast-forward - a job stalled on how a
// model chose to fix something. The lease lets the agent rewrite its own
// push and nothing else: if anyone else has pushed to the branch since, the
// remote is not at lease, and the push is refused.
//
// The token travels in the environment of this one process, as an HTTP header
// scoped to the remote's URL, and is never written to a file.
func push(ctx context.Context, relayDir, remote, head, branch, lease, token string) error {
	ref := "refs/heads/" + branch
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null",
		"push", "--quiet", "--no-verify", "--force-with-lease="+ref+":"+lease, remote, head+":"+ref)
	cmd.Dir = relayDir
	cmd.Env = append(cmd.Environ(), pushEnv(remote, token)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pushEnv is the environment that gives a push its token: git's own
// configuration-by-environment, so the token is not an argument (visible in
// ps to everyone) or a file. Nothing for a remote that is not HTTPS, which is
// a local path in a test.
func pushEnv(remote, token string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if token == "" || !strings.HasPrefix(remote, "https://") {
		return env
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return append(env,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http."+remote+".extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
	)
}

// remoteHead is the commit the remote's branch is at, or empty if it has no
// such branch. Read with no credentials, as the clone is.
func remoteHead(ctx context.Context, remote, branch string) (string, error) {
	out, err := git(ctx, "", "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	return sha, nil
}

// commits is how many commits the workspace's branch has on top of base.
func commits(ctx context.Context, dir, base string) (int, error) {
	out, err := git(ctx, dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// uncommitted is what `git status` says is changed in the workspace's tracked
// files and not committed: empty for a clean tree. Untracked files are not
// counted; the gate cleans them away before it runs.
func uncommitted(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "status", "--porcelain", "--untracked-files=no")
}

// reset puts the workspace back at base, on the branch it has checked out,
// with nothing untracked.
func reset(ctx context.Context, dir, base string) error {
	if _, err := git(ctx, dir, "reset", "--quiet", "--hard", base); err != nil {
		return err
	}
	_, err := git(ctx, dir, "clean", "--quiet", "--force", "-d")
	return err
}

// branchOf is the branch the workspace has checked out, or HEAD if none is.
func branchOf(ctx context.Context, dir string) (string, error) {
	return git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
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
