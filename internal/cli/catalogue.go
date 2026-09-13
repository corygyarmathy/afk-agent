package cli

import (
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// catalogue is the set of transitions this build knows.
//
// Built here, from a literal, rather than assembled at init time by whichever
// packages happen to be linked in. Registration through a package-level map is
// the usual Go shape for this and is deliberately not used: the set of
// transitions would then be a property of the import graph, and
// TestEveryTransitionRunsStandalone could not know what it was meant to check.
//
// It takes the review's dependencies because a transition's Run reaches them.
// Naming a transition does not: a registry built from nil dependencies answers
// every question about names, kinds and states, and only running a transition
// needs them - which is how `afk run` refuses a mistyped name before it has
// opened or resolved anything.
//
// It is a variable so that the tests in this package can put a transition in
// front of the command surface without shipping one; nothing but a test ever
// assigns to it.
var catalogue = func(d *review.Deps) *transition.Registry {
	return transition.MustRegistry(review.Transitions(d)...)
}
