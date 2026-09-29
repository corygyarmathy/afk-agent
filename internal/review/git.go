package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// Git checks a pull request's head out with the git binary.
type Git struct {
	// Remote is the repository to fetch from, and the token the fetch
	// carries.
	Remote git.Remote

	// Relays is the directory the fetch is made in: outside every
	// workspace, which the remote is never reached from (git.Remote's
	// Untrusted).
	Relays string
}

// Checkout fetches refs/pull/<n>/head and checks it out detached in dir. The
// commit it returns is the one actually checked out, which is the head the
// review is of - the pull request may have moved since anyone last read it.
//
// Shallow, because a review reads a head and not its history. The diff against
// the base comes from the tracker rather than from git, which is what lets the
// fetch stay shallow.
//
// The fetch that carries the token is made in a bare relay under Relays, named
// for dir, and the head is brought from there into dir locally, with no token:
// dir is a workspace, where the model's session runs. The relay is made afresh
// and removed afterwards, so one left by a killed run is never what is read.
// Every step is isolated, so no template, hook or other configuration of the
// agent user's is in either repository, and nothing of the token is left in
// dir.
func (g Git) Checkout(ctx context.Context, dir string, number int) (string, error) {
	if g.Relays == "" {
		return "", errors.New("the review's checkout has no directory for its relay")
	}
	// Absolute, since the fetch into dir names it and runs in dir.
	relay, err := filepath.Abs(filepath.Join(g.Relays, filepath.Base(dir)+".git"))
	if err != nil {
		return "", err
	}
	if err := os.RemoveAll(relay); err != nil {
		return "", err
	}
	defer os.RemoveAll(relay)
	const head = "refs/afk/head"
	if _, err := git.RunEnv(ctx, "", git.Isolated, "init", "--quiet", "--bare", relay); err != nil {
		return "", err
	}
	if _, err := g.Remote.Run(ctx, relay, "fetch", "--quiet", "--depth=1", g.Remote.URL, fmt.Sprintf("+refs/pull/%d/head:%s", number, head)); err != nil {
		return "", err
	}

	if _, err := git.RunEnv(ctx, dir, git.Isolated, "init", "--quiet"); err != nil {
		return "", err
	}
	if _, err := git.RunEnv(ctx, dir, git.Isolated, "fetch", "--quiet", "--depth=1", relay, head); err != nil {
		return "", err
	}
	if _, err := git.RunEnv(ctx, dir, git.Isolated, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}
	return git.RunEnv(ctx, dir, git.Isolated, "rev-parse", "HEAD")
}
