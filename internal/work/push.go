package work

import (
	"context"
	"fmt"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Unlanded is a push whose rounds ran out: made Rounds times, and never seen
// on the remote.
type Unlanded struct {
	Rounds int

	// Note is why the last round failed, as the effect kept it.
	Note string
}

// Said is what a hand-back says about a push of head to branch that never
// landed.
func (u Unlanded) Said(head, branch string) string {
	return fmt.Sprintf("The push of `%s` to `%s` was made %d times and never landed.", git.Short(head), branch, u.Rounds)
}

// PushRound is the leased push of head from the job's relay to p's branch, as
// the effect of the next round, or the rounds having run out.
//
// The stem is new with each head, and a head is pushed only by the work that
// made it. The lease is p.Pushed, the commit the agent last saw its own push
// land at, or the head the work started from: a push anyone else made since is
// never overwritten.
func (w Workspace) PushRound(ctx context.Context, s store.Store, rounds int, jobID string, p Progress, head string) (transition.Effect, *Unlanded, error) {
	stem := pushStem(p.Branch, head)
	key, err := transition.Round(ctx, s, stem, 0, rounds)
	if spent, ok := transition.Spent(err); ok {
		return transition.Effect{}, &Unlanded{Rounds: spent.Rounds, Note: transition.Noted(w.NotePath(jobID), stem)}, nil
	}
	if err != nil {
		return transition.Effect{}, nil, err
	}
	relayDir, remote, branch, lease := w.RelayDir(jobID), w.Remote, p.Branch, p.Pushed
	return transition.Effect{Key: key, Do: transition.Noting(w.NotePath(jobID), stem, func(ctx context.Context) error {
		return Push(ctx, relayDir, remote, head, branch, lease)
	})}, nil, nil
}

// PushNote is why the last push of p.Head failed, as the effect kept it, for a
// hand-back to quote.
func (w Workspace) PushNote(jobID string, p Progress) string {
	return transition.Noted(w.NotePath(jobID), pushStem(p.Branch, p.Head))
}

func pushStem(branch, head string) string {
	return fmt.Sprintf("push-%s-%s", branch, head)
}

// Landing is where a push of p.Head is, read back from the remote.
type Landing int

const (
	// NotLanded is the branch still at the lease: the push has not landed
	// yet, and is made again under the next round.
	NotLanded Landing = iota
	// Landed is the branch at the push.
	Landed
	// Moved is the branch at neither the push nor the lease it was pinned
	// to: someone else pushed to it, or deleted it. Every push from here is
	// refused by the lease, so none is made.
	Moved
)

// Land reads the remote's branch back after a push of p.Head, rather than
// trusting the effect that pushed it: the runner commits and then performs,
// so a process killed between the two loses the effect with its key reserved,
// and this is what notices.
//
// Landed, the push is recorded in p as the lease the next push is pinned to,
// and the head CI is watched on from now; seen again, it is left as it was.
// at is where the branch is, for a hand-back to say.
func (w Workspace) Land(ctx context.Context, p *Progress, now time.Time) (landing Landing, at string, err error) {
	at, err = RemoteHead(ctx, w.Remote, p.Branch)
	if err != nil {
		return NotLanded, "", err
	}
	switch {
	case at == p.Head:
		if p.Pushed != at {
			p.Pushed, p.PushedAt = at, now
		}
		return Landed, at, nil
	case at == p.Pushed:
		return NotLanded, at, nil
	default:
		return Moved, at, nil
	}
}
