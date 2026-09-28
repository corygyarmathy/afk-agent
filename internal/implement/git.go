package implement

import (
	"context"
	"strconv"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/work"
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
// The clone and the read of taken branches carry the token as the push does,
// and nothing of it is left in dir: what the clone writes there is the model's
// to read.
func prepare(ctx context.Context, remote git.Remote, dir, prefix string, issue int) (branch, base, into string, err error) {
	into, err = work.Clone(ctx, remote, dir)
	if err != nil {
		return "", "", "", err
	}
	heads, err := remote.Run(ctx, "", "ls-remote", "--heads", remote.URL)
	if err != nil {
		return "", "", "", err
	}
	taken := map[string]bool{}
	for _, line := range strings.Split(heads, "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			taken[strings.TrimPrefix(ref, "refs/heads/")] = true
		}
	}
	// A branch whose whole is still there is taken too: its cut would find
	// the work kept for an earlier one where it keeps its own.
	for k := 1; ; k++ {
		branch = prefix + strconv.Itoa(issue) + "-" + strconv.Itoa(k)
		if !taken[branch] && !taken[wholeBranch(branch)] {
			break
		}
	}
	if _, err := git.Run(ctx, dir, "switch", "--quiet", "--create", branch); err != nil {
		return "", "", "", err
	}
	base, err = git.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", "", err
	}
	return branch, base, into, nil
}

// wholeBranch is where work sent back to be cut is pushed first, as it was, so
// that nothing the cut does can lose it (#127). Not spelled as a branch of the
// agent's (IssueOf): a pull request opened from it is a human's.
func wholeBranch(branch string) string {
	return branch + "-whole"
}
