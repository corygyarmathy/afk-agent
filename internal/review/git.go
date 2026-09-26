package review

import (
	"context"
	"fmt"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// Git checks a pull request's head out with the git binary.
type Git struct {
	// Remote is the repository to fetch from, and the token the fetch
	// carries.
	Remote git.Remote
}

// Checkout fetches refs/pull/<n>/head into dir and checks it out detached. The
// commit it returns is the one actually checked out, which is the head the
// review is of - the pull request may have moved since anyone last read it.
//
// Shallow, because a review reads a head and not its history. The diff against
// the base comes from the tracker rather than from git, which is what lets the
// fetch stay shallow.
//
// Every step is isolated, so no template, hook or other configuration of the
// agent user's is in the repository when the fetch, which carries the token,
// runs there, or when the head is checked out. Nothing of the token is left
// in dir.
func (g Git) Checkout(ctx context.Context, dir string, number int) (string, error) {
	if _, err := git.RunEnv(ctx, dir, git.Isolated, "init", "--quiet"); err != nil {
		return "", err
	}
	if _, err := g.Remote.Run(ctx, dir, "fetch", "--quiet", "--depth=1", g.Remote.URL, fmt.Sprintf("refs/pull/%d/head", number)); err != nil {
		return "", err
	}
	if _, err := git.RunEnv(ctx, dir, git.Isolated, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return git.RunEnv(ctx, dir, git.Isolated, "rev-parse", "HEAD")
}
