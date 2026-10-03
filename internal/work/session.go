package work

import (
	"context"
	"errors"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Model runs one model. opencode.Command is one.
type Model interface {
	Run(ctx context.Context, req opencode.Request) (opencode.Reply, error)
}

// Tier is the candidates a job's sessions run on (ADR 0001 §9), and how far
// down them a run goes.
type Tier struct {
	// Resolve is the ordered candidate list, as of now. A
	// *model.LimitedError defers the job to the reset.
	Resolve func(ctx context.Context) (model.Candidates, error)

	// Bound is how many candidates a run tries before the tier counts as
	// exhausted, and Wait how long an exhausted tier defers. Parameters.
	Bound int
	Wait  time.Duration
}

// Choose is the candidate this run of the job uses. With no candidate to run
// now, ok is false and the result defers the job to deferred.
//
// The stays are the candidates that failed transiently, because that is the
// one stay a session's transition makes. Another way to stay there would move
// the work on to the next candidate as well (#62).
func (t Tier) Choose(ctx context.Context, in transition.In, deferred string) (ref model.Ref, res transition.Result, ok bool, err error) {
	ref, wait, err := model.Choose(ctx, t.Resolve, in.Job.Stays, t.Bound, in.Now, t.Wait)
	if err != nil {
		return model.Ref{}, transition.Result{}, false, err
	}
	if !wait.Until.IsZero() {
		return model.Ref{}, transition.Result{State: deferred, RunAt: wait.Until, Exhausted: wait.Exhausted}, false, nil
	}
	return ref, transition.Result{}, true, nil
}

// Run runs one candidate's session, req. With a reply, ok is true. Without
// one, ok is false and the result is what the job does instead: stay, which
// moves the next run to the next candidate, or defer to deferred when the
// tier has none left. The reply then holds what the failed run spent and
// nothing else, which the caller counts as it would a reply's (#22): a run
// that failed was still paid for.
//
// A session gone with opencode's data - a rebuilt host, say - is run again as
// a fresh one, with the prompt fresh gives. The retry is weaker without it,
// but the job carries on (ADR 0001 §6). fresh is also where the kind forgets
// the session it had recorded.
//
// A transient failure is logged, because nothing else keeps it once the next
// candidate runs. It stays rather than returning an error: an error is an
// attempt, and every other error here is one that is not the model's (ADR 0001
// §10). A tier with no candidate left defers with the failure that ran it out
// (#98).
func (t Tier) Run(ctx context.Context, in transition.In, m Model, req opencode.Request, fresh func() (string, error), stay, deferred string, logf func(format string, a ...any)) (reply opencode.Reply, res transition.Result, ok bool, err error) {
	reply, err = m.Run(ctx, req)
	var gone *opencode.SessionGoneError
	if errors.As(err, &gone) {
		if req.Prompt, err = fresh(); err != nil {
			return opencode.Reply{}, transition.Result{}, false, err
		}
		req.Session = ""
		reply, err = m.Run(ctx, req)
	}
	var transient *opencode.TransientError
	if errors.As(err, &transient) {
		logf("%s: %v", in.Job.ID, transient)
		if wait := model.Failed(ctx, t.Resolve, in.Job.Stays, t.Bound, in.Now, t.Wait, transient); !wait.Until.IsZero() {
			return reply, transition.Result{State: deferred, RunAt: wait.Until, Exhausted: wait.Exhausted}, false, nil
		}
		return reply, transition.Result{State: stay, RunAt: in.Now}, false, nil
	}
	if err != nil {
		return opencode.Reply{}, transition.Result{}, false, err
	}
	return reply, transition.Result{}, true, nil
}
