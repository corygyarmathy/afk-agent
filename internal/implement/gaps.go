package implement

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// stoppedLine is the line the implement skill opens its report with when the
// session stopped on gaps in the ticket: "Stopped on gaps: nothing changed." A
// heading's or emphasis's marks before it are forgiven, and so is case.
var stoppedLine = regexp.MustCompile(`(?i)^[\s#*_>]*stopped on gaps\b`)

// gaps is what a session that stopped on gaps asked: the report after its
// first line, the gaps as questions, each with its recommended answer. ok is
// false for a report that does not open with the line, or has nothing after
// it, which is a session that committed nothing for some other reason.
func gaps(report string) (questions string, ok bool) {
	text := strings.TrimLeft(strings.TrimPrefix(strings.ReplaceAll(report, "\r\n", "\n"), "\uFEFF"), " \t\n")
	first, rest, _ := strings.Cut(text, "\n")
	if !stoppedLine.MatchString(first) {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
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
