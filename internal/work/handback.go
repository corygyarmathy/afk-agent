package work

import (
	"context"
	"fmt"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/spend"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// handBackTail is how much of the gate's output a hand-back quotes. A comment
// is for a human skimming it, and the workspace is gone by then.
const handBackTail = 4 << 10

// HandBack is a job stopping and returning a pull request to a human: the
// words the kind chooses, and the machinery both kinds share.
type HandBack struct {
	// Book is the kind's way to what it owes the tracker.
	Book *owed.Book

	// Label is the hand-back label, applied to the pull request.
	Label string

	// Number is the pull request the hand-back is on.
	Number int

	// Key is what the hand-back is said once under.
	Key string

	// Marker is the hidden line the comment is read back by, and Also any
	// further hidden lines the kind's own read-back needs - a revision's
	// markers, one per command of the send-back.
	Marker string
	Also   string

	// Stopped is the kind's word for what happened, Next its word for what a
	// human can do, Detail what a session said - a revision's points done so
	// far - and Output what a gate, CI or a note said.
	Stopped string
	Next    string
	Detail  string
	Output  string

	// Spent is what the job's runs cost, for the comment's footer: a job
	// that could not finish still spent it (#22).
	Spent spend.Spent

	// HandingBack is the state the job moves to while the hand-back is read
	// back, and Rest the state it comes to rest in.
	HandingBack string
	Rest        string
}

// HandBackPR returns a job to a human after the push: a comment saying what
// was tried, and the hand-back label, on the pull request and nowhere else,
// because by then the work and the failure are both the pull request's
// (dotfiles ADR 0007 §2). Never the hand-off. The pull request stays open:
// closing work a human may want is not the agent's to do.
//
// Keyed by what is said once, which is the kind's to choose. Read back like
// the hand-back on an issue.
func (w Workspace) HandBackPR(ctx context.Context, in transition.In, jobID string, hb HandBack) (transition.Result, error) {
	body := HandBackBody(hb.Marker, hb.Also, hb.Stopped, hb.Detail, hb.Output, hb.Next, hb.Spent)
	if err := w.Clear(jobID); err != nil {
		return transition.Result{}, err
	}
	return hb.Book.Owe(ctx, in, hb.HandingBack, owed.Record{Next: hb.Rest, Items: []owed.Item{
		owed.Comment(fmt.Sprintf("hand-back-pr-%d-%s", hb.Number, hb.Key), hb.Number, hb.Marker, body),
		owed.Label(fmt.Sprintf("hand-back-label-pr-%d-%s", hb.Number, hb.Key), hb.Number, hb.Label),
	}})
}

// HandBackBody is a hand-back comment: what stopped, what a session said, the
// end of the output that said so, what a human can do next, and what the job
// spent, when its runs said.
func HandBackBody(marker, also, stopped, detail, output, next string, spent spend.Spent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", marker)
	if also != "" {
		fmt.Fprintf(&b, "%s", also)
	}
	fmt.Fprintf(&b, "%s\n", stopped)
	if detail = strings.TrimSpace(detail); detail != "" {
		fmt.Fprintf(&b, "\n%s\n", detail)
	}
	if output = strings.TrimSpace(Tail(output, handBackTail)); output != "" {
		fmt.Fprintf(&b, "\nThe end of the last output:\n\n````\n%s\n````\n", output)
	}
	fmt.Fprintf(&b, "\n%s\n", next)
	if footer := spent.Footer(); footer != "" {
		fmt.Fprintf(&b, "\n%s\n", footer)
	}
	return b.String()
}
