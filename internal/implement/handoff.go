package implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
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
	pr, ok, err := d.pullRequestFrom(ctx, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	if !ok {
		// Closed, or merged, by a human while the review was coming.
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	}

	comments, err := d.Tracker.Comments(ctx, pr.Number)
	if err != nil {
		return transition.Result{}, err
	}
	if d.hasReview(comments, p.Pushed) {
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
	}

	// No review job, or one at rest with no review of this head to show
	// for it. Asked for again under the next key, so a request lost to a
	// kill is made again, and one that keeps coming to nothing runs out.
	key, err := d.round(ctx, fmt.Sprintf("review-asked-pr-%d-%s", pr.Number, p.Pushed))
	if err != nil {
		return transition.Result{}, err
	}
	now := in.Now
	wait.Effects = []transition.Effect{{Key: key, Do: func(ctx context.Context) error {
		return d.askReview(ctx, subject, now)
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
	pr, ok, err := d.pullRequestFrom(ctx, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	if !ok {
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	}
	for _, l := range pr.Labels {
		if l == d.HandOffLabel {
			return transition.Result{State: Start}, d.clear(in.Job.ID)
		}
	}
	key, err := d.round(ctx, fmt.Sprintf("hand-off-pr-%d-%s", pr.Number, p.Pushed))
	if err != nil {
		return transition.Result{}, err
	}
	n := pr.Number
	effect := transition.Effect{Key: key, Do: func(ctx context.Context) error { return d.Tracker.Label(ctx, n, d.HandOffLabel) }}
	return transition.Result{State: HandingOff, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// hasReview reports whether the agent has posted a review of head.
func (d *Deps) hasReview(comments []github.Comment, head string) bool {
	marker := review.Marker(head)
	for _, c := range comments {
		if strings.EqualFold(c.Login, d.Login) && strings.Contains(c.Body, marker) {
			return true
		}
	}
	return false
}

// askReview makes the pull request's review job due now, creating it if it is
// not there. A job already queued or held is left alone: that run will review
// the head. The one store write a transition's effect makes, and it is to
// another job: this one's own state is the runner's to write.
func (d *Deps) askReview(ctx context.Context, subject store.Subject, now time.Time) error {
	job, err := d.Store.Ensure(ctx, store.KindReview, subject, review.Start, now)
	if err != nil {
		return err
	}
	if !job.NextRunAt.IsZero() {
		return nil
	}
	job, ok, err := d.Store.Acquire(ctx, job.ID, d.Holder, now, d.LeaseTTL)
	if err != nil || !ok {
		return err
	}
	if !job.NextRunAt.IsZero() {
		return d.Store.Release(ctx, job.ID, d.Holder)
	}
	err = d.Store.Commit(context.WithoutCancel(ctx), store.Commit{
		JobID: job.ID, Holder: d.Holder, State: review.Start, NextRunAt: now, Release: true,
	})
	if err != nil {
		return errors.Join(err, d.Store.Release(context.WithoutCancel(ctx), job.ID, d.Holder))
	}
	return nil
}
