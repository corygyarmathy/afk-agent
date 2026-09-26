package cli

import (
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// commands is the command registry (ADR 0001 §14): the comment commands this
// build answers, the kind of subject each is issued on, the job kind it asks
// for, and the state that kind's jobs start in. A variable for the reason
// catalogue is.
//
// `/implement` is not here yet. Its transitions are registered, so a hand-run
// works, but the kind stops after its claim until #53 lands, and answering the
// command before then would claim work nothing finishes.
var commands = func() []intake.Command {
	return []intake.Command{
		{Word: review.Word, On: store.SubjectPR, Kind: store.KindReview, Start: review.Start},
	}
}

// subjectOf is the kind of subject a job kind's jobs are attached to. `afk run`
// refuses any other before it makes a job, because a job on the wrong kind of
// subject can only fail until it parks.
func subjectOf(k store.Kind) store.SubjectType {
	if k == store.KindImplement {
		return store.SubjectIssue
	}
	return store.SubjectPR
}
