package implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// awaitReview is `implement-review`: ask for the review of the green head, wait
// for it, and hand the pull request off once it is there.
//
// The review is a review job this job makes due, never a /review comment: a
// comment the agent wrote must never be able to instruct the agent (ADR 0001
// §14, as amended for #40). The review job claims the request with a 👀 on the
// pull request's description, which this job wrote.
//
// The hand-off waits for the review, because it says "CI is green and a review
// has been posted" (docs/agents/triage-labels.md). It is a signal, not a
// control: nothing merges on it (ADR 0001 §15).
func (d *Deps) awaitReview(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.lost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	pr, ok, err := d.open(ctx, from(p.Branch))
	if err != nil {
		return transition.Result{}, err
	}
	if !ok {
		// Closed, or merged, by a human while the review was coming.
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	}

	r, err := d.work().AwaitReview(ctx, in, d.reviews(), pr.Number, p.Progress)
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case work.ReviewPosted:
		return transition.Result{State: HandingOff, RunAt: in.Now}, nil
	case work.ReviewHandedBack:
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	case work.ReviewHandBack:
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, r.Reason, r.Output)
	}
	return transition.Result{State: Reviewing, RunAt: r.RunAt, Effects: r.Effects}, nil
}

// reviews is the wait for the review, and its bounds.
func (d *Deps) reviews() work.Reviews {
	return work.Reviews{Comments: d.Tracker, Store: d.Store, Login: d.Login, Ask: d.AskReview, Wait: d.CIWait, Rounds: d.Rounds}
}

// handOff is `implement-hand-off`: the hand-off label on the pull request,
// read back from the tracker.
//
// Its own state for the reason review-verify is one: the runner commits and
// then performs, so a process killed between the two loses the label with its
// key reserved. Coming to rest on the decision would leave the pull request
// reviewed and never handed off, and nothing would look again. This reads the
// label back, and applies it under the next key until it is there, or until
// its rounds run out and the pull request is handed back instead.
func (d *Deps) handOff(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.lost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	pr, ok, err := d.open(ctx, from(p.Branch))
	if err != nil {
		return transition.Result{}, err
	}
	if !ok {
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	}
	for _, l := range pr.Labels {
		if strings.EqualFold(l, d.HandOffLabel) {
			return transition.Result{State: Start}, d.clear(in.Job.ID)
		}
	}
	stem := fmt.Sprintf("hand-off-pr-%d-%s", pr.Number, p.Pushed)
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, fmt.Sprintf("CI is green on `%s` and it has its review, but the `%s` label was applied %d times and never appeared.", git.Short(p.Pushed), d.HandOffLabel, spent.Rounds), transition.Noted(d.notePath(in.Job.ID), stem))
	}
	if err != nil {
		return transition.Result{}, err
	}
	n := pr.Number
	effect := transition.Effect{Key: key, Do: transition.Noting(d.notePath(in.Job.ID), stem, func(ctx context.Context) error { return d.Tracker.Label(ctx, n, d.HandOffLabel) })}
	return transition.Result{State: HandingOff, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}
