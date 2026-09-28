package revise

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/git"
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
	return transition.Result{State: Watching, RunAt: in.Now}, nil
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
