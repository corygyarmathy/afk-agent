package implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// pushTransition is `implement-push`: the denylist, then the push, with nothing
// between the two (dotfiles ADR 0007 §6).
//
// The check is made on the relay's copy of the branch, at the commit the push
// will send, and the push sends that commit by name. Between the two there is
// only the commit of this decision: nothing the model runs, and nothing that
// could move what is pushed.
func (d *Deps) pushTransition(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	ws := d.workspacePath(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !isDir(ws)) {
		// The work is gone before it reached the remote, or with nothing
		// to show that it did. Either way it starts over, on a branch
		// nobody has pushed.
		return transition.Result{State: Implementing, RunAt: in.Now}, d.clear(in.Job.ID)
	}
	if err != nil {
		return transition.Result{}, err
	}

	head, err := relay(ctx, ws, d.relayPath(in.Job.ID), p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	paths, err := touched(ctx, d.relayPath(in.Job.ID), p.Base, head)
	if err != nil {
		return transition.Result{}, err
	}
	if bad := denied(d.Denylist, paths); len(bad) > 0 {
		return d.handBack(in, p, fmt.Sprintf("The work touches %s, which the denylist does not let the agent push.", quoted(bad)), "")
	}

	key, err := d.round(ctx, fmt.Sprintf("push-%s-%s", p.Branch, head))
	if err != nil {
		return transition.Result{}, err
	}
	p.Head = head
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	relayDir := d.relayPath(in.Job.ID)
	effect := transition.Effect{Key: key, Do: func(ctx context.Context) error {
		token, err := d.token(ctx)
		if err != nil {
			return err
		}
		return push(ctx, relayDir, d.Remote, head, p.Branch, p.Pushed, token)
	}}
	return transition.Result{State: Opening, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// openPR is `implement-open`: once the push is on the remote, the pull request.
//
// It reads both back from the tracker rather than trusting the effect that
// made them. The runner commits and then performs, so a process killed between
// the two loses the effect with its key reserved; this is what notices, and
// sends the job round again under the next key.
func (d *Deps) openPR(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		// The state directory lost it. The tracker still says whether the
		// pull request was opened, and if it was, the work goes on from
		// there. If it was not, the work starts over on the next free
		// branch. One the push may have made is left where it is: with
		// the progress went the lease, and without it the agent cannot
		// tell its own push from anyone else's.
		if _, ok, err := d.open(ctx, n); err != nil {
			return transition.Result{}, err
		} else if ok {
			return transition.Result{State: Watching, RunAt: in.Now}, nil
		}
		return transition.Result{State: Implementing, RunAt: in.Now}, d.clear(in.Job.ID)
	}
	if err != nil {
		return transition.Result{}, err
	}
	at, err := remoteHead(ctx, d.Remote, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	if at != p.Head {
		return transition.Result{State: Pushing, RunAt: in.Now}, nil
	}
	if p.Pushed != at {
		// Seen on the remote: the lease the next push is pinned to.
		p.Pushed = at
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
	}

	if _, ok, err := d.pullRequestFrom(ctx, p.Branch); err != nil {
		return transition.Result{}, err
	} else if ok {
		return transition.Result{State: Watching, RunAt: in.Now}, nil
	}

	key, err := d.round(ctx, fmt.Sprintf("pull-request-%s", p.Branch))
	if err != nil {
		return transition.Result{}, err
	}
	is, err := d.Tracker.Issue(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	req := github.NewPullRequest{Title: is.Title, Head: p.Branch, Base: p.Into, Body: description(n, p)}
	effect := transition.Effect{Key: key, Do: func(ctx context.Context) error {
		// The key stops this run opening two. The tracker is what stops a
		// round that follows a slow success from opening another.
		if _, ok, err := d.pullRequestFrom(ctx, p.Branch); err != nil || ok {
			return err
		}
		_, err := d.Tracker.CreatePullRequest(ctx, req)
		return err
	}}
	return transition.Result{State: Opening, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// PRMarker is the hidden line the description of the agent's pull request for
// an issue carries.
func PRMarker(issue int) string {
	return fmt.Sprintf("<!-- afk:implement issue=%d -->", issue)
}

// description is the pull request's body: the issue it closes, what the
// session said it did, and where the review will be.
func description(n int, p progress) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nCloses #%d.\n\n", PRMarker(n), n)
	if p.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", p.Summary)
	}
	b.WriteString("Written by the agent. CI decides whether it is correct; an advisory review will be posted here as a comment once CI is green. Merging is yours.\n")
	return b.String()
}

// pullRequestFrom finds the agent's open pull request from branch.
func (d *Deps) pullRequestFrom(ctx context.Context, branch string) (github.PullRequest, bool, error) {
	prs, err := d.Tracker.OpenPullRequests(ctx)
	if err != nil {
		return github.PullRequest{}, false, err
	}
	for _, pr := range prs {
		if pr.HeadRef == branch && strings.EqualFold(pr.Login, d.Login) {
			return pr, true, nil
		}
	}
	return github.PullRequest{}, false, nil
}

// round is the key for the next attempt at an effect: the first of
// <stem>-<i> not yet reserved. Deterministic across replays of the same
// round, and new for a round that follows one whose effect was lost.
func (d *Deps) round(ctx context.Context, stem string) (string, error) {
	bound := d.Bound
	if bound < 1 {
		bound = 1
	}
	for i := range bound {
		key := fmt.Sprintf("%s-%d", stem, i)
		reserved, err := d.Store.Reserved(ctx, key)
		if err != nil {
			return "", err
		}
		if !reserved {
			return key, nil
		}
	}
	return "", fmt.Errorf("%s was tried %d times and never took effect", stem, bound)
}

func (d *Deps) token(ctx context.Context) (string, error) {
	if d.Token == nil {
		return "", nil
	}
	return d.Token(ctx)
}

func quoted(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = "`" + p + "`"
	}
	return strings.Join(q, ", ")
}
