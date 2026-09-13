package cli

import (
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// commands is the command registry (ADR 0001 §14): the comment commands this
// build answers, the job kind each asks for, and the state that kind's jobs
// start in. A variable for the reason catalogue is.
var commands = func() []intake.Command {
	return []intake.Command{
		{Word: review.Word, Kind: store.KindReview, Start: review.Start},
	}
}
