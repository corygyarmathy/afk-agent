package implement

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// questionsFile is where, in the workspace's .git, a session that stopped on
// gaps in the issue writes its questions, each with its recommended answer
// (#199). The file's being there is the stop: nothing is read from the
// wording of the session's report, which is the vendored skill's to change.
const questionsFile = "afk-questions.md"

// readQuestions is what a session that stopped on gaps asked, or nothing.
func readQuestions(ws string) (string, error) {
	text, err := work.ReadGitFile(ws, questionsFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(text, "\uFEFF")), nil
}

// handBackGaps is the hand-back of a session that stopped on gaps: on the
// issue, with nothing pushed, carrying the session's questions in full rather
// than the end of its output (#199). Answered with `/implement <answers>`, the
// answers reach the next session as instructions, the more recent word than
// the issue. The agent does not edit the issue.
//
// Unattended intake does not take the issue again, because its job is still
// in the store, so a gap cannot loop: only a command runs it again.
func (d *Deps) handBackGaps(ctx context.Context, in transition.In, p progress, questions string) (transition.Result, error) {
	marker := handBackMarker(in.Job.Subject.Number, p, p.Nonce)
	stopped := "I stopped without opening a pull request. The session checked the issue before its first edit and found gaps it could not settle with nobody to ask, so it changed nothing. Its questions, each with its recommended answer:"
	next := fmt.Sprintf("Nothing was pushed. Answer with `%s <answers>`, and the next session reads your answers as the more recent word than the issue. Or edit the issue and `%s` again, or take it by hand.", Word, Word)
	body := func(q string) string {
		return work.HandBackBody(marker, "", stopped, q, "", next, p.Spent)
	}
	if work.OverLimit(body(questions)) {
		d.logf("%s: the session's questions are over GitHub's %d characters, so the hand-back cuts them at a line", in.Job.ID, github.BodyLimit)
		questions = cut(questions, github.BodyLimit-utf8.RuneCountInString(body("")))
	}
	return d.oweIssue(ctx, in, p, body(questions))
}

// cutNote ends questions cut to fit a comment.
const cutNote = "\n\n(Cut here: the rest of the session's questions are over what GitHub takes in a comment.)"

// cut is as many of text's whole lines as fit in limit characters with the
// note that says the rest was cut.
func cut(text string, limit int) string {
	limit -= utf8.RuneCountInString(cutNote) + 2
	var b strings.Builder
	used := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		n := utf8.RuneCountInString(line)
		if used+n > limit {
			break
		}
		b.WriteString(line)
		used += n
	}
	return strings.TrimRight(b.String(), "\n") + cutNote
}
