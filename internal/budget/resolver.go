package budget

import "github.com/corygyarmathy/afk-agent/internal/model"

// Resolver is this observation in the shape the model resolver takes
// (model.Budget).
//
// The two halves of ADR 0001 §11 meet here and are different things. Admission
// is about the pool starting work at all and is decided before a job is
// dispatched; the resolver's budget is about one resolution inside a transition
// that is already running - a job in flight when the window closes, or a
// hand-invocation, which admission never sees. Both read the same observation,
// and this is the conversion rather than a second reading of it.
//
// It lives in this package and not in model so that the resolver stays pure:
// nothing in model's import graph reaches the network, and a conversion is not
// worth changing that for.
func (s State) Resolver() model.Budget {
	return model.Budget{Limited: s.Limited(), ResetsAt: s.ResetsAt()}
}
