package implement

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// watch is `implement-watch`: CI's reading of the pushed head (work.Watch),
// which decides correctness where the local gate only decided whether to push
// (dotfiles ADR 0007 §3). The watch, its bounds and the fix count are shared
// with revise; where each answer leaves the job is this kind's.
func (d *Deps) watch(ctx context.Context, in transition.In) (transition.Result, error) {
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
		// Closed, or merged, by a human. That is their decision about the
		// work, and there is nothing to say about it.
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	}
	if r, ok := over(in, p); ok {
		// A failed correction: put back at the head the review read, where
		// CI was green before the review was asked for and is not watched
		// again, or failed here and the move that followed lost.
		return r, nil
	}
	r, err := d.work().Watch(ctx, in, d.ci(), pr.Number, &p.Progress)
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case work.CIPending:
		return transition.Result{State: Watching, RunAt: r.RunAt}, nil
	case work.CIGreen:
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	case work.CIHandBack:
		if p.Correction.Running() && !r.Moved {
			return d.failCorrection(ctx, in, p, r.Reason)
		}
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, r.Reason, r.Output)
	}
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Implementing, RunAt: in.Now}, nil
}

// ci is the CI watch and its bounds, as this kind's parameters set them.
func (d *Deps) ci() work.CI {
	return work.CI{Checks: d.Tracker, Wait: d.CIWait, Ceiling: d.CICeiling, Fixes: d.CIFixes, Log: d.Log}
}

// lost is work past its push whose workspace or progress went with the state
// directory: in a watch, or in a fix. A red run could not go back to the
// session that wrote the branch, and a fix would start a new branch
// beside the pull request. The pull request is handed back instead - once per
// head - or, if there is none, the job rests.
func (d *Deps) lost(ctx context.Context, in transition.In) (transition.Result, error) {
	pr, ok, err := d.open(ctx, d.forIssue(in.Job.Subject.Number))
	if err != nil || !ok {
		return transition.Result{State: Start}, errors.Join(err, d.clear(in.Job.ID))
	}
	return d.handBackPR(ctx, in, progress{Progress: work.Progress{Branch: pr.HeadRef}}, pr.Number, "lost-"+pr.HeadSHA,
		"The agent lost its record of the work - its state directory was wiped - so it cannot watch CI or fix what CI finds.", "")
}

// handBackPR returns the work to a human after the push: a comment saying
// what was tried, and the hand-back label, on the pull request and nowhere
// else, because by then the work and the failure are both the pull request's
// (dotfiles ADR 0007 §2). Never the hand-off. The pull request stays open:
// closing work a human may want is not the agent's to do.
//
// Keyed by what is said once: the progress's nonce, or the head a lost record
// is handed back at. Read back like the hand-back on the issue.
func (d *Deps) handBackPR(ctx context.Context, in transition.In, p progress, pr int, key, reason, output string) (transition.Result, error) {
	return d.work().HandBackPR(ctx, in, in.Job.ID, work.HandBack{
		Book:        d.book(),
		Label:       d.HandBackLabel,
		Number:      pr,
		Key:         key,
		Marker:      handBackMarker(in.Job.Subject.Number, p, key),
		Stopped:     "I stopped before handing this pull request off. " + reason,
		Next:        fmt.Sprintf("The pull request stays open: finish the branch by hand, or close it and `%s` again on #%d.", Word, in.Job.Subject.Number),
		Output:      output,
		Spent:       p.Spent,
		HandingBack: HandingBack,
		Rest:        Start,
	})
}
