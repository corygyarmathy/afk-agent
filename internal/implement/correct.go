package implement

import (
	"context"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// correct starts correction c of the advisory review of the pushed head: back
// to the session that wrote the branch, given the findings (#193). From there
// it goes the way a fix does - the gate, the denylist, the leased push, CI -
// with the gate's attempts and CI's fixes afresh, since a correction is a new
// convergence on both, as a cut is. The size signal never cuts it: the pull
// request is open.
func (d *Deps) correct(ctx context.Context, in transition.In, p progress, c correction.Correction) (transition.Result, error) {
	p.Correction = &c
	p.Attempts, p.Fixes, p.FixedHead = 0, 0, ""
	p.Failure, p.Why = "", ""
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Implementing, RunAt: in.Now}, nil
}

// corrected is the correction done with, in `implement-review`: the advisory
// review edited to say how it ended, and then the hand-off. Corrected, the
// review links the commit that corrected each finding; failed, the branch is
// back at the head the review read, and the findings are advice.
func (d *Deps) corrected(ctx context.Context, in transition.In, p progress, pr int) (transition.Result, error) {
	c := p.Correction
	switch {
	case c.Running() && p.Pushed == c.Reviewed:
		// Begun, and the commit that sent it to the session never
		// happened: it goes there now.
		return transition.Result{State: Implementing, RunAt: in.Now}, nil
	case !c.Running() && p.Pushed != c.Reviewed:
		// Failed, and the push back to the reviewed head not seen yet.
		return transition.Result{State: Pushing, RunAt: in.Now}, nil
	}
	var commits map[int]string
	if c.Running() {
		var err error
		if commits, err = correction.Commits(ctx, d.work().RelayDir(in.Job.ID), c.Reviewed, p.Pushed); err != nil {
			return transition.Result{}, err
		}
	}
	logf := func(format string, a ...any) { d.logf("%s: "+format, append([]any{in.Job.ID}, a...)...) }
	effect, ok, err := d.editor().Edit(ctx, pr, *c, p.Pushed, commits, logf)
	if err != nil {
		return transition.Result{}, err
	}
	if ok {
		return transition.Result{State: Reviewing, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
	}
	return transition.Result{State: HandingOff, RunAt: in.Now}, nil
}

// stop is the work stopped by something it did: a correction under way fails,
// and anything else is handed back.
func (d *Deps) stop(ctx context.Context, in transition.In, p progress, reason, output string) (transition.Result, error) {
	if p.Correction.Running() {
		return d.failCorrection(ctx, in, p, reason)
	}
	return d.handBack(ctx, in, p, reason, output)
}

// failCorrection is a correction that cannot be finished: the gate red at its
// last attempt, CI red past its fixes, nothing committed, or anything else the
// correction's own work stopped on. It is not a hand-back. The workspace goes
// back to the head the review read, which is pushed back under the lease if the
// correction had pushed past it, and the pull request is handed off with the
// findings left as advice, marked as a correction that failed. Someone else's
// push is theirs, and still hands back.
//
// Logged, because the review says why in a sentence and the output that said
// it goes nowhere else.
func (d *Deps) failCorrection(ctx context.Context, in transition.In, p progress, why string) (transition.Result, error) {
	if !d.work().Exists(in.Job.ID) {
		return d.lost(ctx, in)
	}
	c := p.Correction
	if err := work.Restore(ctx, d.work().Dir(in.Job.ID), p.Branch, c.Reviewed); err != nil {
		return transition.Result{}, err
	}
	from, err := work.PushFrom(ctx, d.Store, p.Branch, c.Reviewed)
	if err != nil {
		return transition.Result{}, err
	}
	d.logf("%s: the correction of the review of `%s` failed, so the pull request goes back to it: %s", in.Job.ID, git.Short(c.Reviewed), why)
	c.Failed, c.From = why, from
	p.Failure, p.Why = "", ""
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	if p.Pushed == c.Reviewed {
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	return transition.Result{State: Pushing, RunAt: in.Now}, nil
}

// rewrote is why a correction that rewrote the head the review read failed.
func rewrote(c *correction.Correction) string {
	return "The correction rewrote `" + git.Short(c.Reviewed) + "`, the head the review read, which a correction never does."
}

// reviewed is the head the advisory review read, for a correction's prompt.
func reviewed(p progress) string {
	if p.Correction == nil {
		return ""
	}
	return p.Correction.Reviewed
}

// editor is the edit of the advisory review once a correction is done with.
func (d *Deps) editor() correction.Editor {
	return correction.Editor{Tracker: d.Tracker, Store: d.Store, Login: d.Login, Repo: d.Repo, Rounds: d.Rounds}
}
