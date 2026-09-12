package cli

import "github.com/corygyarmathy/afk-agent/internal/transition"

// catalogue is the set of transitions this build knows.
//
// Built here, from a literal, rather than assembled at init time by whichever
// packages happen to be linked in. Registration through a package-level map is
// the usual Go shape for this and is deliberately not used: the set of
// transitions would then be a property of the import graph, and
// TestEveryTransitionRunsStandalone could not know what it was meant to check.
//
// It is empty today. The first entry is `/review` (#3); the runner, the
// dispatcher and this command surface are what it will be entered into, and
// they are complete without it. Everything in this package is tested against
// registries built in the tests, so nothing here is waiting on that issue -
// but the standalone-invocation test is vacuous until it lands, and says so.
// It is a variable so that the tests in this package can put a transition in
// front of the command surface without shipping one; nothing but a test ever
// assigns to it.
var catalogue = func() *transition.Registry {
	return transition.MustRegistry()
}
