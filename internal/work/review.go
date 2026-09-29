package work

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Reviews is the wait for the advisory review of a green head, and its bounds:
// the same for every kind that hands a pull request off.
//
// The review is a review job the kind makes due, never a /review comment: a
// comment the agent wrote must never be able to instruct the agent (ADR 0001
// §14, as amended for #40). The review job claims the request on what the
// asking job wrote.
type Reviews struct {
	Comments interface {
		Comments(ctx context.Context, number int) ([]github.Comment, error)
	}

	// Store is read, never written: where the review job is, and which round
	// of the request is next.
	Store store.Store

	// Login is the agent's own account: whose comment is a review.
	Login string

	// Ask makes the review job for a pull request due (ReviewAsker).
	Ask func(ctx context.Context, pr store.Subject, now time.Time) error

	// Wait is how long the job waits before it looks again, and Rounds how
	// many times the review is asked for before one that never comes is
	// handed back. Parameters.
	Wait   time.Duration
	Rounds int
}

// ReviewState is what the wait found.
type ReviewState int

const (
	// ReviewPending is a review on its way, or asked for again: looked at
	// again at Result.RunAt, performing Result.Effects.
	ReviewPending ReviewState = iota
	// ReviewPosted is the review of the pushed head on the pull request.
	ReviewPosted
	// ReviewHandedBack is the review job's own hand-back of the pushed head
	// on the pull request. Asking again would write the same review, and
	// post it into whatever stopped the last one: the hand-back is the pull
	// request's, with the same label and the error that stopped it, and a
	// second would say less, twice.
	ReviewHandedBack
	// ReviewHandBack is the work returned to a human on the pull request:
	// someone else pushed, the review job parked, or the review was asked for
	// until its rounds ran out. Reason and Output say why.
	ReviewHandBack
)

// ReviewResult is what AwaitReview decided. The kind moves the job; the words
// around Reason are its own.
type ReviewResult struct {
	State ReviewState

	// The next look at a pending review: the job stays where it is.
	RunAt   time.Time
	Effects []transition.Effect

	Reason string
	Output string
}

// AwaitReview asks for the review of the head the agent pushed to pull request
// pr, once CI is green on it, and waits for it.
//
// Someone else's push is theirs, as it is while CI runs: the review job
// reviews the pull request's head, so a review of the agent's would never come.
func (w Workspace) AwaitReview(ctx context.Context, in transition.In, r Reviews, pr int, p Progress) (ReviewResult, error) {
	at, err := RemoteHead(ctx, w.Remote, p.Branch)
	if err != nil {
		return ReviewResult{}, err
	}
	if at != p.Pushed {
		return ReviewResult{State: ReviewHandBack, Reason: fmt.Sprintf("Someone else pushed to `%s` after CI went green: it is at `%s`, not at `%s` where the agent left it, and the agent does not hand off anyone else's work.", p.Branch, git.Short(at), git.Short(p.Pushed))}, nil
	}

	comments, err := r.Comments.Comments(ctx, pr)
	if err != nil {
		return ReviewResult{}, err
	}
	if review.Reviewed(comments, r.Login, p.Pushed) {
		return ReviewResult{State: ReviewPosted}, nil
	}
	if review.HandedBack(comments, r.Login, p.Pushed) {
		return ReviewResult{State: ReviewHandedBack}, nil
	}

	wait := ReviewResult{State: ReviewPending, RunAt: in.Now.Add(r.Wait)}
	subject := store.Subject{Type: store.SubjectPR, Number: pr}
	rj, err := r.Store.Job(ctx, store.ID(store.KindReview, subject))
	switch {
	case errors.Is(err, store.ErrNoJob):
	case err != nil:
		return ReviewResult{}, err
	case !rj.NextRunAt.IsZero() || (rj.Lease != nil && !rj.Lease.Expired(in.Now)):
		// Queued, or running now: the review is on its way.
		return wait, nil
	case rj.State != review.Start:
		// Parked where it failed. Starting it over would throw its state
		// away, and it is the operator's to look at.
		return ReviewResult{State: ReviewHandBack, Reason: fmt.Sprintf("The review job for this pull request failed and stopped in `%s`, so no review of `%s` is coming.", rj.State, git.Short(p.Pushed))}, nil
	}

	// No review job, or one at rest with no review of this head to show
	// for it. Asked for again under the next key, so a request lost to a
	// kill is made again, and one that keeps coming to nothing runs out and
	// is handed back.
	stem := fmt.Sprintf("review-asked-pr-%d-%s", pr, p.Pushed)
	key, err := transition.Round(ctx, r.Store, stem, 0, r.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return ReviewResult{State: ReviewHandBack, Reason: fmt.Sprintf("CI is green on `%s`, but its review was asked for %d times and never came.", git.Short(p.Pushed), spent.Rounds), Output: transition.Noted(w.NotePath(in.Job.ID), stem)}, nil
	}
	if err != nil {
		return ReviewResult{}, err
	}
	now := in.Now
	wait.Effects = []transition.Effect{{Key: key, Do: transition.Noting(w.NotePath(in.Job.ID), stem, func(ctx context.Context) error {
		return r.Ask(ctx, subject, now)
	})}}
	return wait, nil
}

// ReviewAsker is Reviews.Ask, asking under a's lease. A job already queued or
// held is left alone: that run will review the head. So is one parked away
// from start, which AwaitReview hands back.
func ReviewAsker(a transition.Armer) func(ctx context.Context, pr store.Subject, now time.Time) error {
	return func(ctx context.Context, pr store.Subject, now time.Time) error {
		return a.Ask(ctx, store.KindReview, pr, review.Start, now)
	}
}
