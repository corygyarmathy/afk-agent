package implement

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/handoff"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// awaitReview is `implement-review`: ask for the review of the green head, wait
// for it, and hand the pull request off once it is there (package handoff). The
// review job claims the request with a 👀 on the pull request's description,
// which this job wrote.
//
// A review with a Correctness or Standards finding sends the work back to its
// session first, for one correction (package correction). Once the correction
// is green, or has failed and the branch is back at the head the review read,
// the job is here again: the review is edited to say so, and then the pull
// request is handed off. Nothing asks for a second review.
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

	if p.Correction != nil {
		return d.corrected(ctx, in, p, pr.Number)
	}
	r, err := d.handOffDeps().AwaitReview(ctx, in, pr.Number, p.Progress.Progress)
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case handoff.Done:
		if c, ok := correction.Begin(r.Review, p.Pushed); ok {
			return d.correct(ctx, in, p, c)
		}
		return transition.Result{State: HandingOff, RunAt: in.Now}, nil
	case handoff.HandedBack:
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	case handoff.HandBack:
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, r.Reason, r.Output)
	}
	return transition.Result{State: Reviewing, RunAt: r.RunAt, Effects: r.Effects}, nil
}

// handOff is `implement-hand-off`: the hand-off label on the pull request,
// read back from the tracker (package handoff). Its own state for the reason
// review-verify is one.
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
	r, err := d.handOffDeps().HandOff(ctx, in, pr, p.Pushed, fmt.Sprintf("hand-off-pr-%d-%s", pr.Number, p.Pushed))
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case handoff.Done:
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	case handoff.HandBack:
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, r.Reason, r.Output)
	}
	return transition.Result{State: HandingOff, RunAt: r.RunAt, Effects: r.Effects}, nil
}

// handOffDeps is the hand-off's view of this kind, and its bounds.
func (d *Deps) handOffDeps() handoff.Deps {
	return handoff.Deps{Work: d.work(), Tracker: d.Tracker, Store: d.Store, Login: d.Login, Ask: d.AskReview, Label: d.HandOffLabel, Wait: d.CIWait, Rounds: d.Rounds}
}
