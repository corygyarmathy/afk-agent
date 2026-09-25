package cli

import (
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// deps is what the transitions of each job kind reach. A kind whose field is
// nil is not configured in this process.
type deps struct {
	review    *review.Deps
	implement *implement.Deps
}

// catalogue is the set of transitions this build knows.
//
// Built here, from a literal, rather than assembled at init time by whichever
// packages happen to be linked in. Registration through a package-level map is
// the usual Go shape for this and is deliberately not used: the set of
// transitions would then be a property of the import graph, and
// TestEveryTransitionRunsStandalone could not know what it was meant to check.
//
// It takes each kind's dependencies because a transition's Run reaches them.
// Naming a transition does not: a registry built from nil answers every
// question about names, kinds and states, and only running a transition needs
// them - which is how `afk run` refuses a mistyped name before it has opened or
// resolved anything.
//
// Given dependencies, a kind whose own are nil is left out: a worker pool with
// no implement configured parks an implement job, rather than running a
// transition that has nothing to reach.
//
// It is a variable so that the tests in this package can put a transition in
// front of the command surface without shipping one; nothing but a test ever
// assigns to it.
var catalogue = func(d *deps) *transition.Registry {
	if d == nil {
		return transition.MustRegistry(append(review.Transitions(nil), implement.Transitions(nil)...)...)
	}
	var ts []transition.Transition
	if d.review != nil {
		ts = append(ts, review.Transitions(d.review)...)
	}
	if d.implement != nil {
		ts = append(ts, implement.Transitions(d.implement)...)
	}
	return transition.MustRegistry(ts...)
}
