// Package handoff is how every kind that hands a pull request off ends: the
// advisory review of the head the agent pushed, asked for and waited for once
// CI is green on it, and then the hand-off label, read back from the tracker.
//
// The review is a review job the kind makes due, never a /review comment: a
// comment the agent wrote must never be able to instruct the agent (ADR 0001
// §14, as amended for #40). The review job claims the request on what the
// asking job wrote.
//
// The hand-off waits for the review, because it says "CI is green and a review
// has been posted" (docs/agents/triage-labels.md). It is a signal, not a
// control: nothing merges on it (ADR 0001 §15).
//
// The label is read back, rather than trusted once applied, because the runner
// commits and then performs: a process killed between the two loses the label
// with its key reserved. Coming to rest on the decision would leave the pull
// request reviewed and never handed off, and nothing would look again.
package handoff

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// Tracker is what the hand-off reads and writes. *github.Client is one.
type Tracker interface {
	Comments(ctx context.Context, number int) ([]github.Comment, error)
	Label(ctx context.Context, number int, label string) error
}

// Deps is the hand-off's view of the kind handing off, and its bounds.
type Deps struct {
	// Work is the kind's workspace: the remote the head is read from, and
	// where an effect's last error is noted.
	Work    work.Workspace
	Tracker Tracker

	// Store is read, never written: where the review job is, and which round
	// of an effect is next.
	Store store.Store

	// Login is the agent's own account: whose comment is a review.
	Login string

	// Ask makes the review job for a pull request due (Asker).
	Ask func(ctx context.Context, pr store.Subject, now time.Time) error

	// Label is the hand-off label. A parameter.
	Label string

	// Wait is how long the job waits before it looks at the review again,
	// and Rounds how many times the review is asked for, or the label
	// applied, before one that never comes is handed back. Parameters.
	Wait   time.Duration
	Rounds int
}

// State is what the hand-off found.
type State int

const (
	// Pending is a review or a label on its way, or asked for again: looked
	// at again at Result.RunAt, performing Result.Effects.
	Pending State = iota
	// Done is the review of the pushed head, or the hand-off label, on the
	// pull request.
	Done
	// HandedBack is the review job's own hand-back of the pushed head on the
	// pull request. Asking again would write the same review, and post it
	// into whatever stopped the last one: the hand-back is the pull
	// request's, with the same label and the error that stopped it, and a
	// second would say less, twice.
	HandedBack
	// HandBack is the work returned to a human on the pull request: someone
	// else pushed, the review job parked, or the review or the label was
	// asked for until its rounds ran out. Reason and Output say why.
	HandBack
)

// Result is what the hand-off decided. The kind moves the job; the words
// around Reason are its own.
type Result struct {
	State State

	// The next look at a pending review or label: the job stays where it is.
	RunAt   time.Time
	Effects []transition.Effect

	Reason string
	Output string

	// Review is the review of the pushed head, when AwaitReview finds it
	// Done: what a correction of its findings reads (package correction).
	Review github.Comment
}

// AwaitReview asks for the review of the head the agent pushed to pull request
// pr, once CI is green on it, and waits for it.
//
// Someone else's push is theirs, as it is while CI runs: the review job
// reviews the pull request's head, so a review of the agent's would never come.
func (d Deps) AwaitReview(ctx context.Context, in transition.In, pr int, p work.Progress) (Result, error) {
	at, err := work.RemoteHead(ctx, d.Work.Remote, p.Branch)
	if err != nil {
		return Result{}, err
	}
	if at != p.Pushed {
		return Result{State: HandBack, Reason: fmt.Sprintf("Someone else pushed to `%s` after CI went green: it is at `%s`, not at `%s` where the agent left it, and the agent does not hand off anyone else's work.", p.Branch, git.Short(at), git.Short(p.Pushed))}, nil
	}

	comments, err := d.Tracker.Comments(ctx, pr)
	if err != nil {
		return Result{}, err
	}
	if r, ok := review.Review(comments, d.Login, p.Pushed); ok {
		return Result{State: Done, Review: r}, nil
	}
	if review.HandedBack(comments, d.Login, p.Pushed) {
		return Result{State: HandedBack}, nil
	}

	wait := Result{State: Pending, RunAt: in.Now.Add(d.Wait)}
	subject := store.Subject{Type: store.SubjectPR, Number: pr}
	rj, err := d.Store.Job(ctx, store.ID(store.KindReview, subject))
	switch {
	case errors.Is(err, store.ErrNoJob):
	case err != nil:
		return Result{}, err
	case !rj.NextRunAt.IsZero() || (rj.Lease != nil && !rj.Lease.Expired(in.Now)):
		// Queued, or running now: the review is on its way.
		return wait, nil
	case rj.State != review.Start:
		// Parked where it failed. Starting it over would throw its state
		// away, and it is the operator's to look at.
		return Result{State: HandBack, Reason: fmt.Sprintf("The review job for this pull request failed and stopped in `%s`, so no review of `%s` is coming.", rj.State, git.Short(p.Pushed))}, nil
	}

	// No review job, or one at rest with no review of this head to show
	// for it. Asked for again under the next key, so a request lost to a
	// kill is made again, and one that keeps coming to nothing runs out and
	// is handed back.
	stem := fmt.Sprintf("review-asked-pr-%d-%s", pr, p.Pushed)
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return Result{State: HandBack, Reason: fmt.Sprintf("CI is green on `%s`, but its review was asked for %d times and never came.", git.Short(p.Pushed), spent.Rounds), Output: transition.Noted(d.Work.NotePath(in.Job.ID), stem)}, nil
	}
	if err != nil {
		return Result{}, err
	}
	now := in.Now
	wait.Effects = []transition.Effect{{Key: key, Do: transition.Noting(d.Work.NotePath(in.Job.ID), stem, func(ctx context.Context) error {
		return d.Ask(ctx, subject, now)
	})}}
	return wait, nil
}

// HandOff applies the hand-off label to pull request pr, reviewed at pushed,
// until it is read back there, or until its rounds run out and the pull
// request is handed back instead. The label's keys are made from stem, which
// is the kind's.
func (d Deps) HandOff(ctx context.Context, in transition.In, pr github.PullRequest, pushed, stem string) (Result, error) {
	for _, l := range pr.Labels {
		if strings.EqualFold(l, d.Label) {
			return Result{State: Done}, nil
		}
	}
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return Result{State: HandBack, Reason: fmt.Sprintf("CI is green on `%s` and it has its review, but the `%s` label was applied %d times and never appeared.", git.Short(pushed), d.Label, spent.Rounds), Output: transition.Noted(d.Work.NotePath(in.Job.ID), stem)}, nil
	}
	if err != nil {
		return Result{}, err
	}
	n := pr.Number
	effect := transition.Effect{Key: key, Do: transition.Noting(d.Work.NotePath(in.Job.ID), stem, func(ctx context.Context) error { return d.Tracker.Label(ctx, n, d.Label) })}
	return Result{State: Pending, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// Asker is Deps.Ask, asking under a's lease. A job already queued or held is
// left alone: that run will review the head. So is one parked away from start,
// which AwaitReview hands back.
func Asker(a transition.Armer) func(ctx context.Context, pr store.Subject, now time.Time) error {
	return func(ctx context.Context, pr store.Subject, now time.Time) error {
		return a.Ask(ctx, store.KindReview, pr, review.Start, now)
	}
}
