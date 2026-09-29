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

	// The denylist is checked on what the push sends, commit by commit:
	// everything the branch at the lease does not have. What the pull
	// request already carried, and anyone else's push the revision was
	// replayed onto, is on the remote and was not the agent's to push.
	paths, err := work.Touched(ctx, relayDir, p.Pushed, head)
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
	case landing == work.Moved && at == "":
		return d.handBack(ctx, in, p, p.overwrote(at), d.work().PushNote(in.Job.ID, p.Progress))
	case landing == work.Moved:
		// Someone else pushed: the revision goes on top of their push.
		return transition.Result{State: Replaying, RunAt: in.Now}, nil
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

// replay is `revise-replay`: someone else pushed to the branch while the
// revision ran, and the revision's own commits go on top of their push, to be
// gated and pushed again under a lease pinned to it. Nothing they pushed, and
// nothing the send-back was written against, is rewritten. A push on top of the
// revision's own, once it landed, is no push during it: the revision moves on
// to be watched. It hands back when
// their push dropped what the agent last saw there, when a replay conflicts,
// and when the revision has already been replayed as many times as it may be.
//
// Their head is fetched into the relay - a repository the agent owns and has
// isolated - and the replay is made there, so no process that carries the
// token, and none that runs a hook, runs in the workspace (#41's comment on
// #84).
//
// A replay is counted when the lease moves on to the head it is made onto,
// and saved before the workspace changes: run again after a kill, the same
// head is not counted twice, and a workspace already on top of it is left as
// it is (work.Replay).
func (d *Deps) replay(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !d.work().Exists(in.Job.ID)) {
		return d.handBackLost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}

	// Read before the fetch, so that a branch that is gone is a hand-back
	// and a fetch that fails is an error, made again.
	if at, err := work.RemoteHead(ctx, d.Remote, p.Branch); err != nil {
		return transition.Result{}, err
	} else if at == "" {
		return d.handBack(ctx, in, p, p.overwrote(at), "")
	}
	relayDir := d.work().RelayDir(in.Job.ID)
	at, err := work.FetchInto(ctx, relayDir, d.Remote, "refs/heads/"+p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	// A push on top of the revision's own, after it landed and before the
	// agent read it back - a bot that pushes after every push - is not one
	// made during the revision: the revision is on the branch, and lands as
	// it would have. The relay holds only what the branch has, so a head it
	// has is one the branch has. It is checked before a replay is counted,
	// so that a replay counted and then killed is not taken for it.
	if p.Head != "" && p.Head != p.Pushed {
		if has, err := work.HasCommit(ctx, relayDir, p.Head); err != nil {
			return transition.Result{}, err
		} else if has {
			if landed, err := work.Ancestor(ctx, relayDir, p.Head, at); err != nil {
				return transition.Result{}, err
			} else if landed {
				p.Pushed, p.PushedAt = p.Head, in.Now
				if err := d.save(in.Job.ID, p); err != nil {
					return transition.Result{}, err
				}
				return transition.Result{State: Watching, RunAt: in.Now}, nil
			}
		}
	}
	// Their push has to be on top of the lease: the head the send-back was
	// written against, the agent's own last push, or the last push the
	// revision was replayed onto. One that dropped it rewrote what was
	// read, and a replay onto it would push over that.
	if has, err := work.HasCommit(ctx, relayDir, p.Pushed); err != nil {
		return transition.Result{}, err
	} else if !has {
		return d.handBack(ctx, in, p, p.overwrote(at), "")
	}
	if kept, err := work.Ancestor(ctx, relayDir, p.Pushed, at); err != nil {
		return transition.Result{}, err
	} else if !kept {
		return d.handBack(ctx, in, p, p.overwrote(at), "")
	}

	if at != p.Pushed {
		if p.Replays >= d.Replays {
			return d.handBack(ctx, in, p, fmt.Sprintf("Someone else changed `%s` while the revision ran: %s. The revision had already been replayed onto pushes made during it as many times as it may be (%d), so it was not pushed, and the agent does not push over anyone else's work.", p.Branch, p.moved(at), d.Replays), "")
		}
		p.Replays++
		p.Pushed = at
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
	}
	if _, conflict, err := work.Replay(ctx, d.work().Dir(in.Job.ID), relayDir, p.Branch, p.Pushed); err != nil {
		return transition.Result{}, err
	} else if conflict != nil {
		return d.handBack(ctx, in, p, fmt.Sprintf("Someone else pushed to `%s` while the revision ran, and the revision does not replay onto their push: %s Nothing was pushed, and the agent does not push over anyone else's work.", p.Branch, conflict.Said(p.Pushed)), "")
	}
	return transition.Result{State: Gating, RunAt: in.Now}, nil
}

// overwrote is the hand-back's words for a branch the revision cannot be
// replayed onto: deleted, or pushed over so that it no longer has the head the
// agent last saw there.
func (p progress) overwrote(at string) string {
	return fmt.Sprintf("Someone else changed `%s` while the revision ran: %s. The revision was not pushed, and the agent does not push over anyone else's work.", p.Branch, p.moved(at))
}

// moved says where the branch is, when it is at neither the revision's push
// nor its lease. Before the revision's first push lands the lease is the head
// the send-back was written against, and after a replay it is the push the
// revision was replayed onto: the agent left neither there.
func (p progress) moved(at string) string {
	switch {
	case at == "" || p.Pushed == p.Head:
		return work.Where(at, p.Pushed)
	case p.Pushed == p.Read:
		return fmt.Sprintf("it is at `%s`, not at `%s` where the send-back was written", git.Short(at), git.Short(p.Read))
	}
	return fmt.Sprintf("it is at `%s`, not at `%s`, the push the revision was last replayed onto", git.Short(at), git.Short(p.Pushed))
}
