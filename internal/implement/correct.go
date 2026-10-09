package implement

import (
	"context"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/handoff"
	"github.com/corygyarmathy/afk-agent/internal/transition"
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

// corrected is the correction in `implement-review`, where it begins and ends
// (correction.Next): sent on to the session or the push back if the move there
// was lost, and once done with, the advisory review edited to say how it ended
// and the hand-off. Corrected, the review links the commit that corrected each
// finding; failed, the branch is back at the head the review read, and the
// findings are advice. Someone else's push since is theirs, and hands back, as
// it does while the review is awaited.
func (d *Deps) corrected(ctx context.Context, in transition.In, p progress, pr int) (transition.Result, error) {
	if s := correction.Next(p.Correction, p.Pushed); s != correction.Edit {
		return transition.Result{State: step(s), RunAt: in.Now}, nil
	}
	if r, err := d.handOffDeps().Kept(ctx, p.Progress.Progress); err != nil {
		return transition.Result{}, err
	} else if r.State == handoff.HandBack {
		return d.handBackPR(ctx, in, p, pr, p.Nonce, r.Reason, r.Output)
	}
	logf := func(format string, a ...any) { d.logf("%s: "+format, append([]any{in.Job.ID}, a...)...) }
	effect, ok, err := d.editor().Edit(ctx, pr, *p.Correction, p.Pushed, d.work().RelayDir(in.Job.ID), logf)
	if err != nil {
		return transition.Result{}, err
	}
	if ok {
		return transition.Result{State: Reviewing, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
	}
	return transition.Result{State: HandingOff, RunAt: in.Now}, nil
}

// over is a failed correction met again in a state that failed it, which has
// nothing left to do for it: ok, and where the job goes instead.
func over(in transition.In, p progress) (transition.Result, bool) {
	s := p.Correction.Over(p.Pushed)
	return transition.Result{State: step(s), RunAt: in.Now}, s != correction.Making
}

// step is the state a correction's step s goes on in.
func step(s correction.Step) string {
	switch s {
	case correction.Session:
		return Implementing
	case correction.PushBack:
		return Pushing
	}
	return Reviewing
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
	if err := c.Fail(ctx, d.Store, d.work().Dir(in.Job.ID), p.Branch, why); err != nil {
		return transition.Result{}, err
	}
	d.logf("%s: the correction of the review of `%s` failed, so the pull request goes back to it: %s", in.Job.ID, git.Short(c.Reviewed), why)
	p.Failure, p.Why = "", ""
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	r, _ := over(in, p)
	return r, nil
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
