package revise

import (
	"context"
	"errors"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// watch is `revise-watch`: CI's reading of the revision's pushed head, the
// same watch implement runs (work.Watch). A red run goes back to the
// revision's session with what CI said, under the rules the revision ran
// under, and its push goes through the same gate, denylist and lease. Out of
// fixes, or past the ceiling, the revision is handed back with the points done
// so far.
func (d *Deps) watch(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	// Before the progress is read: clearing it is how a closed pull request
	// comes to rest, and a run killed or failed after the clear but before
	// its commit comes back here with the progress gone. That is not a lost
	// revision to hand back on a pull request nobody is reading.
	if pr.State != "open" {
		// Closed, or merged, by a human. That is their decision about the
		// pull request, and there is nothing to say about it.
		return transition.Result{State: Start}, errors.Join(d.work().Clear(in.Job.ID), d.clear(in.Job.ID))
	}
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.handBackLost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	r, err := d.work().Watch(ctx, in, d.ci(), n, &p.Progress)
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case work.CIPending:
		return transition.Result{State: Watching, RunAt: r.RunAt}, nil
	case work.CIGreen:
		// The reply, the advisory review and the hand-off are #149's.
		return transition.Result{State: Replying, RunAt: in.Now}, nil
	case work.CIHandBack:
		return d.handBack(ctx, in, p, r.Reason, r.Output)
	}
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Revising, RunAt: in.Now}, nil
}

// ci is the CI watch and its bounds: implement's parameters, since one CI
// serves both kinds.
func (d *Deps) ci() work.CI {
	return work.CI{Checks: d.Tracker, Wait: d.CIWait, Ceiling: d.CICeiling, Fixes: d.CIFixes, Log: d.Log}
}
