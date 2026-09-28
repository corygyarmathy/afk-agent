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
// limited would refuse the candidate list admission had just allowed. A
// limited window that is waived is left out exactly as Admit leaves it out.
//
// It lives in this package and not in model so that the resolver stays pure:
// nothing in model's import graph reaches the network, and a conversion is not
// worth changing that for.
func (s State) Resolver(waivers Waivers, now time.Time) model.Budget {
	var (
		limited bool
		resets  time.Time
	)
	for _, w := range s.Windows {
		if !w.Limited() {
			continue
		}
		if _, ok := waivers.Waived(w, now); ok {
			continue
		}
		// The last of the limited windows, as State.ResetsAt is: the account
		// stays limited until every one that is not waived has reopened.
		if !limited || w.ResetsAt.After(resets) {
			limited, resets = true, w.ResetsAt
		}
	}
	return model.Budget{Limited: limited, ResetsAt: resets}
}

// Resolver is s in the model resolver's shape, with this observer's waivers
// applied at the observer's own clock.
//
// The conversion is offered from the observer too so that resolution and
// admission waive the same windows of the same observation: a caller that
// reached for State.Resolver with a clock of its own could waive a window that
// had lapsed for admission, or miss one it had not.
func (o *Observer) Resolver(s State) model.Budget {
	return s.Resolver(o.Waivers, o.now())
}
