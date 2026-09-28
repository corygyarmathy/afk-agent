package work

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/glob"
)

// Clone clones the remote's default branch into dir, which must not exist or
// be empty, and returns that branch. Only the default branch is fetched: a job
// that needs another ref fetches it through the relay, so that no
// token-carrying fetch but this first one ever runs in dir.
//
// The clone carries the token as every read does, and nothing of it is left in
// dir: what the clone writes there is the model's to read.
func Clone(ctx context.Context, remote git.Remote, dir string) (into string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if _, err := remote.Run(ctx, "", "clone", "--quiet", "--no-tags", "--single-branch", remote.URL, abs); err != nil {
		return "", err
	}
	into, err = git.Run(ctx, dir, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(into, "origin/"), nil
}

// Relay copies the workspace's branch into relayDir, a bare repository only
// the agent writes, and returns the commit it copied. The push is made from
// there and never from the workspace.
//
// The workspace's .git is the model's to write, and git obeys it: a pre-push
// hook, a core.fsmonitor command, or a url.insteadOf that sends the push
// somewhere else would each run with - or send away - the token the push
// carries. A fetch from the workspace into a repository the agent made reads
// only its objects: upload-pack takes no hooks from the repository it serves.
//
// The relay is made again on every push, so one found already in place - which
// something other than this push could have made, or configured - is never
// what the push reads. Its git reads no global or system configuration either:
// a session running as the agent's user could write those as well.
func Relay(ctx context.Context, workspace, relayDir, branch string) (string, error) {
	if err := os.RemoveAll(relayDir); err != nil {
		return "", err
	}
	if _, err := git.RunEnv(ctx, "", git.Isolated, "init", "--quiet", "--bare", relayDir); err != nil {
		return "", err
	}
	ref := "refs/heads/" + branch
	if _, err := git.RunEnv(ctx, relayDir, git.Isolated, "fetch", "--quiet", "--no-tags", "--force", workspace, "+"+ref+":"+ref); err != nil {
		return "", err
	}
	return git.RunEnv(ctx, relayDir, git.Isolated, "rev-parse", ref)
}

// FetchInto makes relayDir a bare repository only the agent writes, fetches ref
// from the remote into it carrying the token, and returns the commit ref is at.
//
// It is the inverse of Relay, for a job that starts from a head the remote
// already has: a fetch that carries the token runs in a repository the agent
// owns and has isolated, never inside the workspace, whose .git/config is the
// model's to write (#134).
func FetchInto(ctx context.Context, relayDir string, remote git.Remote, ref string) (string, error) {
	if err := os.RemoveAll(relayDir); err != nil {
		return "", err
	}
	if _, err := git.RunEnv(ctx, "", git.Isolated, "init", "--quiet", "--bare", relayDir); err != nil {
		return "", err
	}
	if _, err := remote.Run(ctx, relayDir, "fetch", "--quiet", "--no-tags", "--force", remote.URL, "+"+ref+":"+ref); err != nil {
		return "", err
	}
	return git.RunEnv(ctx, relayDir, git.Isolated, "rev-parse", ref)
}

// Import brings ref from the agent's relay into the workspace, locally and
// with no token, and leaves branch checked out at commit. The recorded commit
// is what a revision starts from, which may be behind the ref's tip; the
// workspace gets the ref's objects either way, and the push's lease is what
// will refuse a head that moved on.
//
// The fetch carries no token, so nothing the workspace's .git/config says can
// send one anywhere. It reads objects alone (upload-pack).
func Import(ctx context.Context, relayDir, workspace, branch, ref, commit string) error {
	if _, err := git.RunEnv(ctx, workspace, git.Isolated, "fetch", "--quiet", "--no-tags", "--force", relayDir, "+"+ref+":refs/afk/import"); err != nil {
		return err
	}
	if _, err := git.RunEnv(ctx, workspace, git.Isolated, "checkout", "--quiet", "--force", "-B", branch, commit); err != nil {
		return err
	}
	return nil
}

// Touched is every path a commit in base..head adds, changes or deletes,
// commit by commit rather than in the net diff: a file added and then removed
// again is still in the history the push sends. Renames are a deletion and an
// addition, so both of their names are here. A merge commit is diffed against
// each of its parents, so a path the merge itself adds is here too: `git log`
// lists none for a merge by default.
func Touched(ctx context.Context, dir, base, head string) ([]string, error) {
	out, err := git.RunEnv(ctx, dir, git.Isolated, "log", "--no-renames", "--diff-merges=separate", "--name-only", "--format=", "-z", base+".."+head)
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

// Push sends head to the remote's branch from the relay, leased on lease: the
// commit the agent last saw its own push land at, or empty for a branch that
// must not exist yet (ADR 0001, the amendment of 2026-09-25).
//
// A session may amend or rebase commits the agent already pushed, and a plain
// push would then be refused as not a fast-forward - a job stalled on how a
// model chose to fix something. The lease lets the agent rewrite its own
// push and nothing else: if anyone else has pushed to the branch since, the
// remote is not at lease, and the push is refused.
//
// The token travels as every read's does, and a refusal of it is reported the
// same way (git.Remote).
func Push(ctx context.Context, relayDir string, remote git.Remote, head, branch, lease string) error {
	ref := "refs/heads/" + branch
	_, err := remote.Run(ctx, relayDir, "-c", "core.hooksPath=/dev/null",
		"push", "--quiet", "--no-verify", "--force-with-lease="+ref+":"+lease, remote.URL, head+":"+ref)
	return err
}

// RemoteHead is the commit the remote's branch is at, or empty if it has no
// such branch. Read as the push is made, so that both reach the same remote.
func RemoteHead(ctx context.Context, remote git.Remote, branch string) (string, error) {
	out, err := remote.Run(ctx, "", "ls-remote", remote.URL, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	return sha, nil
}

// Ancestor reports whether ancestor is an ancestor of commit in the repository
// at dir. A revision's push must keep the head the send-back was written
// against: a session that rewrote it broke that, and the job hands back rather
// than pushing a history the operator cannot compare.
func Ancestor(ctx context.Context, dir, ancestor, commit string) (bool, error) {
	out, err := git.RunEnv(ctx, dir, git.Isolated, "merge-base", ancestor, commit)
	if err != nil {
		// No merge base at all is not an ancestor either.
		return false, nil
	}
	return out == ancestor, nil
}

// Commits is how many commits the workspace's branch has on top of base.
func Commits(ctx context.Context, dir, base string) (int, error) {
	out, err := git.Run(ctx, dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// Uncommitted is what `git status` says is changed in the workspace's tracked
// files and not committed: empty for a clean tree. Untracked files are not
// counted; the gate cleans them away before it runs.
func Uncommitted(ctx context.Context, dir string) (string, error) {
	return git.Run(ctx, dir, "status", "--porcelain", "--untracked-files=no")
}

// Reset puts the workspace back at base, on the branch it has checked out,
// with nothing untracked.
func Reset(ctx context.Context, dir, base string) error {
	if _, err := git.Run(ctx, dir, "reset", "--quiet", "--hard", base); err != nil {
		return err
	}
	_, err := git.Run(ctx, dir, "clean", "--quiet", "--force", "-d")
	return err
}

// BranchOf is the branch the workspace has checked out, or HEAD if none is.
func BranchOf(ctx context.Context, dir string) (string, error) {
	return git.Run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// MergeBase is the commit a branch diverged from into, three-dot, which is the
// base of the pull request's diff.
func MergeBase(ctx context.Context, dir, into, head string) (string, error) {
	return git.RunEnv(ctx, dir, git.Isolated, "merge-base", into, head)
}

// Diff is the pull request's diff from base to head as GitHub shows it:
// three-dot, so it starts where the branch diverted.
func Diff(ctx context.Context, dir, base, head string) (string, error) {
	return git.RunEnv(ctx, dir, git.Isolated, "diff", "--no-color", base+"..."+head)
}

// ValidDenylist reports the first pattern that is not a well-formed glob
// (package glob), so a typo is a refusal at startup rather than a pattern
// that silently denies nothing.
func ValidDenylist(denylist []string) error {
	if len(denylist) == 0 {
		return fmt.Errorf("the denylist is empty")
	}
	return glob.Valid("denylist", denylist)
}

// Denied is the paths of paths the denylist matches, for a hand-back that
// names them.
func Denied(denylist, paths []string) []string {
	return glob.Matching(denylist, paths)
}
