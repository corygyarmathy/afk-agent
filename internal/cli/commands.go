package cli

import (
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// commands is the command registry (ADR 0001 §14): the comment commands this
// build answers, the job kind each asks for, and the state that kind's jobs
// start in. A variable for the reason catalogue is.
//
// The start state is the review transition's to name, and that transition is
// #29. Until it is registered, a review job intake makes due is a job no
// transition runs from, and the pool parks it.
var commands = func() []intake.Command {
	return []intake.Command{
		{Word: "/review", Kind: store.KindReview, Start: "start"},
	}
}
