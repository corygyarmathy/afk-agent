package cli

import (
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
)

// commands is the command registry (ADR 0001 §14): the comment commands this
// build answers, the kind of subject each is issued on, the job kind it asks
// for, the state that kind's jobs start in, and whether a submitted review may
// issue it too. A variable for the reason
// catalogue is.
var commands = func() []intake.Command {
	return []intake.Command{
		{Word: review.Word, On: store.SubjectPR, Kind: store.KindReview, Start: review.Start},
		{Word: implement.Word, On: store.SubjectIssue, Kind: store.KindImplement, Start: implement.Start},
		{Word: revise.Word, On: store.SubjectPR, Kind: store.KindRevise, Start: revise.Start, ByReview: true},
	}
}

// unattended is what an issue carrying the eligibility label, label, is taken
// for with nobody asking: the job `/implement` would make.
func unattended(label string) intake.Unattended {
	return intake.Unattended{Label: label, Kind: store.KindImplement, Start: implement.Start}
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
