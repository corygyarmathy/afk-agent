package revise

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/size"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// push is `revise-push`: the revision's commits, on top of the head the
// send-back was written against, pushed under a lease pinned to the head the
// agent last saw the branch at.
//
// The denylist is checked on the relay's copy of the branch, at the commit the
// push will send, and the push sends that commit by name. Nothing runs between
// the two.
func (d *Deps) push(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !d.work().Exists(in.Job.ID)) {
		return d.handBackLost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}

	relayDir := d.work().RelayDir(in.Job.ID)
	head, err := work.Relay(ctx, d.work().Dir(in.Job.ID), relayDir, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}

	// A revision adds commits on top of the head the send-back was written
	// against, and never rewrites it: the operator read that head, and the
	// compare link starts from it. The gate checked it too; this is the
	// check on the commit that is sent. The revision's own commits after it
	// may be rewritten, under the lease.
	if kept, err := work.Ancestor(ctx, relayDir, p.Read, head); err != nil {
		return transition.Result{}, err
	} else if !kept {
		return d.handBack(ctx, in, p, p.rewrote(), "")
	}

	// The denylist is checked on what the revision adds, commit by commit.
	// What the pull request already carried is on the remote, and was not
	// the agent's to push.
	paths, err := work.Touched(ctx, relayDir, p.Read, head)
	if err != nil {
		return transition.Result{}, err
	}
	if bad := work.Denied(d.Denylist, paths); len(bad) > 0 {
		return d.handBack(ctx, in, p, fmt.Sprintf("The revision touches %s, which the denylist does not let the agent push.", work.Quoted(bad)), "")
	}

	// Measured here, on the commit the push sends and in the relay, where
	// nothing the session wrote into its .git is read.
	if err := d.measure(ctx, in.Job.Subject.Number, relayDir, head, &p); err != nil {
		return transition.Result{}, err
	}

	effect, unlanded, err := d.work().PushRound(ctx, d.Store, d.Rounds, in.Job.ID, p.Progress, head)
	if err != nil {
		return transition.Result{}, err
	}
	if unlanded != nil {
		return d.handBack(ctx, in, p, unlanded.Said(head, p.Branch), unlanded.Note)
	}
	p.Head = head
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Pushed, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// pushed is `revise-pushed`: the push read back from the remote before the
// revision moves on. A push the lease will always refuse is not sent round: it
// is handed back, with the points done so far.
func (d *Deps) pushed(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.handBackLost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	if p.Pushed == p.Head {
		// Landed already, and back for the description's edit.
		return d.resensitize(ctx, in, p)
	}
	switch landing, at, err := d.work().Land(ctx, &p.Progress, in.Now); {
	case err != nil:
		return transition.Result{}, err
	case landing == work.Moved:
		return d.handBack(ctx, in, p, fmt.Sprintf("Someone else changed `%s` while the revision ran: %s. The revision was not pushed, and the agent does not push over anyone else's work.", p.Branch, p.moved(at)), d.work().PushNote(in.Job.ID, p.Progress))
	case landing == work.NotLanded:
		// Again, under the next key.
		return transition.Result{State: Pushing, RunAt: in.Now}, nil
	}
	// Seen on the remote: what CI is watched on, and the lease the
	// revision's own later pushes are pinned to (#147).
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return d.resensitize(ctx, in, p)
}

// resensitize brings the pull request's sensitive line to what the push just
// seen on the remote touches, and leaves the rest of its description as it was
// (work.Resensitize). The edit is read back here, in pushed: the push has
// landed, so it is not read back again, and a push someone else makes after
// it is not taken for one made during the revision. An edit that never lands
// is logged and costs the revision nothing.
func (d *Deps) resensitize(ctx context.Context, in transition.In, p progress) (transition.Result, error) {
	pr, err := d.Tracker.PullRequest(ctx, in.Job.Subject.Number)
	if err != nil {
		return transition.Result{}, err
	}
	logf := func(format string, a ...any) { d.logf("%s: "+format, append([]any{in.Job.ID}, a...)...) }
	effect, ok, err := work.Resensitize(ctx, d.Store, d.Rounds, d.Tracker, pr, p.Branch, p.Head, p.Sensitive, logf)
	if err != nil {
		return transition.Result{}, err
	}
	if !ok {
		return transition.Result{State: Watching, RunAt: in.Now}, nil
	}
	return transition.Result{State: Pushed, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// measure keeps in p the size of the whole pull request at head, and the
// sensitive paths it touches, both as GitHub shows its diff: from where head
// meets its base branch's current tip. Never from a base recorded when the
// work started: the operator may have rebased the pull request onto a newer
// tip before sending it back, or a point may have asked for that, and the
// commits between the two are not the pull request's.
//
// The base branch is read from the pull request, which the operator may have
// changed, and fetched into the relay, where the token may go.
func (d *Deps) measure(ctx context.Context, n int, relayDir, head string, p *progress) error {
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return err
	}
	into := pr.BaseRef
	if into == "" {
		into = p.Into
	}
	tip, err := work.FetchAlso(ctx, relayDir, d.Remote, "refs/heads/"+into)
	if err != nil {
		return fmt.Errorf("the pull request's base branch `%s` could not be fetched: %w", into, err)
	}
	from, err := work.MergeBase(ctx, relayDir, tip, head)
	if err != nil {
		return err
	}
	c, err := size.Measure(ctx, relayDir, from, head)
	if err != nil {
		return err
	}
	p.Lines, p.Tests = c.Lines, c.Tests

	// Recomputed with each push: a revision that newly touches a sensitive
	// path adds it, and one that stops touching it takes it off.
	p.Sensitive = nil
	if len(d.Sensitive) > 0 {
		paths, err := sensitive.Changed(ctx, relayDir, tip, head)
		if err != nil {
			return err
		}
		p.Sensitive = sensitive.Touches(d.Sensitive, paths)
	}
	return nil
}

// moved says where the branch is, when it is at neither the revision's push
// nor its lease. Before the revision's first push lands the lease is the head
// the send-back was written against, which the agent did not leave there.
func (p progress) moved(at string) string {
	if at != "" && p.Pushed == p.Read {
		return fmt.Sprintf("it is at `%s`, not at `%s` where the send-back was written", git.Short(at), git.Short(p.Read))
	}
	return work.Where(at, p.Pushed)
}
