package revise

import (
	"context"

	"github.com/corygyarmathy/afk-agent/internal/correction"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/handoff"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// correct starts correction c of the advisory review of the revision's delta:
// back to the revision's session, given the findings, the same way
// implement's goes back to its own (#193). The gate's attempts and CI's fixes
// start afresh for it.
func (d *Deps) correct(ctx context.Context, in transition.In, p progress, c correction.Correction) (transition.Result, error) {
	p.Correction = &c
	p.Attempts, p.Fixes, p.FixedHead = 0, 0, ""
	p.Failure, p.Why = "", ""
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Revising, RunAt: in.Now}, nil
}

// corrected is the correction in `revise-review`, where it begins and ends,
// as implement's is (correction.Next): the advisory review edited to say how
// it ended, and then the hand-off. The reply is not edited: it answers the
// send-back, at the head the review read. Someone else's push since is
// theirs, and hands back.
func (d *Deps) corrected(ctx context.Context, in transition.In, p progress) (transition.Result, error) {
	if s := correction.Next(p.Correction, p.Pushed); s != correction.Edit {
		return transition.Result{State: step(s), RunAt: in.Now}, nil
	}
	if r, err := d.handOffDeps().Kept(ctx, p.Progress); err != nil {
		return transition.Result{}, err
	} else if r.State == handoff.HandBack {
		return d.handBackReplied(ctx, in, p, r.Reason, r.Output)
	}
	logf := func(format string, a ...any) { d.logf("%s: "+format, append([]any{in.Job.ID}, a...)...) }
	effect, ok, err := d.editor().Edit(ctx, in.Job.Subject.Number, *p.Correction, p.Pushed, d.work().RelayDir(in.Job.ID), logf)
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
		return Revising
	case correction.PushBack:
		return Pushing
	}
	return Reviewing
}

// stop is the revision stopped by something it did: a correction under way
// fails, and anything else is handed back.
func (d *Deps) stop(ctx context.Context, in transition.In, p progress, reason, output string) (transition.Result, error) {
	if p.Correction.Running() {
		return d.failCorrection(ctx, in, p, reason)
	}
	return d.handBack(ctx, in, p, reason, output)
}

// failCorrection is a correction that cannot be finished, as implement's is:
// the workspace goes back to the head the review read, which is pushed back
// under the lease if the correction had pushed past it, and the pull request
// is handed off with the findings left as advice. Not a hand-back.
func (d *Deps) failCorrection(ctx context.Context, in transition.In, p progress, why string) (transition.Result, error) {
	if !d.work().Exists(in.Job.ID) {
		return d.handBackLost(ctx, in)
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
