package budget

import (
	"time"

	"github.com/corygyarmathy/afk-agent/internal/model"
)

// Resolver is this observation in the shape the model resolver takes
// (model.Budget), with the waivers in force applied first.
//
// The two halves of ADR 0001 §11 meet here and are different things. Admission
// is about the pool starting work at all and is decided before a job is
// dispatched; the resolver's budget is about one resolution inside a transition
// that is already running - a job in flight when the window closes, or a
// hand-invocation, which admission never sees. Both read the same observation,
// and this is the conversion rather than a second reading of it.
//
// The waiver has to bind here as well as at admission: a window the operator
// waived admits the work, and a resolver that still read the same window as
// limited would refuse the candidate list admission had just allowed. Both
// leave the waived windows out through State.Waive, so they leave out the same
// ones.
//
// It lives in this package and not in model so that the resolver stays pure:
// nothing in model's import graph reaches the network, and a conversion is not
// worth changing that for.
func (s State) Resolver(waivers Waivers, now time.Time) model.Budget {
	s, _ = s.Waive(waivers, now)
	return model.Budget{Limited: s.Limited(), ResetsAt: s.ResetsAt()}
}

// Resolver is s in the model resolver's shape, with this observer's waivers
// applied at the observer's own clock.
//
// The conversion is offered from the observer too so that resolution and
// admission apply the same waivers against the same clock. They need not read
// the same observation: resolution runs later than admission, inside a job
// already started, and a caller observes afresh for it. If a waiver lapses or
// the observation refreshes in between, resolution sees a limit admission did
// not, and the LimitedError that follows defers the job to the reset - the
// later reading is the truer one, and the job should defer.
func (o *Observer) Resolver(s State) model.Budget {
	return s.Resolver(o.Waivers, o.now())
}
