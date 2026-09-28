package revise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// push is `revise-push`: the revision's commits, on top of the head the
// send-back was written against, pushed under a lease pinned to that head.
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
	// compare link starts from it. A session that rebased or amended it
	// broke the one history rule there is.
	if ok, err := work.Ancestor(ctx, relayDir, p.Pushed, head); err != nil {
		return transition.Result{}, err
	} else if !ok {
		return d.handBack(ctx, in, p, fmt.Sprintf("The revision rewrote `%s`, the head the send-back was written against, which a revision never does. Nothing was pushed.", git.Short(p.Pushed)), "")
	}

	// The denylist is checked on every commit the branch carries, not only
	// the revision's own: the push sends the whole branch.
	paths, err := work.Touched(ctx, relayDir, p.Base, head)
	if err != nil {
		return transition.Result{}, err
	}
	if bad := work.Denied(d.Denylist, paths); len(bad) > 0 {
		return d.handBack(ctx, in, p, fmt.Sprintf("The revision touches %s, which the denylist does not let the agent push.", quoted(bad)), "")
	}

	p.Head = head
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	stem := fmt.Sprintf("push-%s-%s", p.Branch, head)
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return d.handBack(ctx, in, p, fmt.Sprintf("The push of `%s` to `%s` was made %d times and never landed.", git.Short(head), p.Branch, spent.Rounds), transition.Noted(d.notePath(in.Job.ID), stem))
	}
	if err != nil {
		return transition.Result{}, err
	}
	effect := transition.Effect{Key: key, Do: transition.Noting(d.notePath(in.Job.ID), stem, func(ctx context.Context) error {
		// The lease is pinned to the head the revision started from: a
		// push someone else made while the revision ran is never
		// overwritten, it refuses the lease, and the job hands back.
		return work.Push(ctx, relayDir, d.Remote, head, p.Branch, p.Pushed)
	})}
	return transition.Result{State: Pushed, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// pushed is `revise-pushed`: the push read back from the remote before the
// revision moves on.
//
// It reads the branch rather than trusting the effect that pushed it: the
// runner commits and then performs, so a process killed between the two loses
// the effect with its key reserved, and this is what notices and sends the job
// round again under the next key. A push the lease will always refuse is not
// sent round: it is handed back.
func (d *Deps) pushed(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.handBackLost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	at, err := work.RemoteHead(ctx, d.Remote, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	switch {
	case at == p.Head:
		// Seen on the remote: what CI is watched on, and the lease the
		// revision's own later pushes would be pinned to (#147).
		p.Pushed, p.PushedAt = at, in.Now
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
		return transition.Result{State: Watching, RunAt: in.Now}, nil
	case at != p.Pushed:
		// Neither the agent's push nor the lease it was pinned to:
		// someone else pushed to the branch, or deleted it. The revision
		// is never pushed over their work.
		return d.handBack(ctx, in, p, fmt.Sprintf("Someone else changed `%s` while the revision ran: it is at `%s`, not at `%s` where the send-back was written. The revision was not pushed, and the agent does not push over anyone else's work.", p.Branch, where(at, p.Pushed), git.Short(p.Pushed)), transition.Noted(d.notePath(in.Job.ID), fmt.Sprintf("push-%s-%s", p.Branch, p.Head)))
	default:
		// The push did not land yet: again, under the next key.
		return transition.Result{State: Pushing, RunAt: in.Now}, nil
	}
}

// where says where a branch the agent was about to push is, when it is not
// where the agent left it.
func where(at, lease string) string {
	switch {
	case at == "":
		return "it has been deleted"
	case lease == "":
		return fmt.Sprintf("it is at `%s`, which the agent did not push", git.Short(at))
	}
	return fmt.Sprintf("it is at `%s`, not at `%s` where the agent left it", git.Short(at), git.Short(lease))
}

// quoted names paths in backticks for a hand-back.
func quoted(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = "`" + p + "`"
	}
	return strings.Join(q, ", ")
}
