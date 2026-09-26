package implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
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

	// Someone else's push is theirs, as it is while CI runs: the review job
	// reviews the pull request's head, so a review of the agent's would never
	// come.
	at, err := remoteHead(ctx, d.Remote, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	if at != p.Pushed {
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, fmt.Sprintf("Someone else pushed to `%s` after CI went green: it is at `%s`, not at `%s` where the agent left it, and the agent does not hand off anyone else's work.", p.Branch, git.Short(at), git.Short(p.Pushed)), "")
	}

	comments, err := d.Tracker.Comments(ctx, pr.Number)
	if err != nil {
		return transition.Result{}, err
	}
	if review.Reviewed(comments, d.Login, p.Pushed) {
		return transition.Result{State: HandingOff, RunAt: in.Now}, nil
	}

	wait := transition.Result{State: Reviewing, RunAt: in.Now.Add(d.CIWait)}
	subject := store.Subject{Type: store.SubjectPR, Number: pr.Number}
	rj, err := d.Store.Job(ctx, store.ID(store.KindReview, subject))
	switch {
	case errors.Is(err, store.ErrNoJob):
	case err != nil:
		return transition.Result{}, err
	case !rj.NextRunAt.IsZero() || (rj.Lease != nil && !rj.Lease.Expired(in.Now)):
		// Queued, or running now: the review is on its way.
		return wait, nil
	case rj.State != review.Start:
		// Parked where it failed. Starting it over would throw its state
		// away, and it is the operator's to look at.
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, fmt.Sprintf("The review job for this pull request failed and stopped in `%s`, so no review of `%s` is coming.", rj.State, git.Short(p.Pushed)), "")
	}

	// No review job, or one at rest with no review of this head to show
	// for it. Asked for again under the next key, so a request lost to a
	// kill is made again, and one that keeps coming to nothing runs out.
	key, err := transition.Round(ctx, d.Store, fmt.Sprintf("review-asked-pr-%d-%s", pr.Number, p.Pushed), d.Bound)
	if err != nil {
		return transition.Result{}, err
	}
	now := in.Now
	wait.Effects = []transition.Effect{{Key: key, Do: func(ctx context.Context) error {
		return d.AskReview(ctx, subject, now)
	}}}
	return wait, nil
}

// handOff is `implement-hand-off`: the hand-off label on the pull request,
// read back from the tracker.
//
// Its own state for the reason review-verify is one: the runner commits and
// then performs, so a process killed between the two loses the label with its
// key reserved. Coming to rest on the decision would leave the pull request
// reviewed and never handed off, and nothing would look again. This reads the
// label back, and applies it under the next key until it is there.
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
	key, err := transition.Round(ctx, d.Store, fmt.Sprintf("hand-off-pr-%d-%s", pr.Number, p.Pushed), d.Bound)
	if err != nil {
		return transition.Result{}, err
	}
	n := pr.Number
	effect := transition.Effect{Key: key, Do: func(ctx context.Context) error { return d.Tracker.Label(ctx, n, d.HandOffLabel) }}
	return transition.Result{State: HandingOff, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// ReviewAsker is Deps.AskReview, asking under a's lease. A job already queued
// or held is left alone: that run will review the head. So is one parked away
// from start, which awaitReview hands back.
func ReviewAsker(a intake.Armer) func(ctx context.Context, pr store.Subject, now time.Time) error {
	return func(ctx context.Context, pr store.Subject, now time.Time) error {
		return a.Ask(ctx, store.KindReview, pr, review.Start, now)
	}
}
