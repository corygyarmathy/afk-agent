package revise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/handoff"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/size"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// sections is the session's part of the reply, in the order it is posted.
// Nothing else the session wrote is: narration, a verdict or a self-rating is
// not what the reply is for, and the prompt asks for none of it.
var sections = []string{"Points", "Suggested follow-ups", "Not verified"}

// reply owes the tracker the revision's reply, once CI is green on its final
// head: one comment answering every command of the send-back, read back before
// the review is asked for.
func (d *Deps) reply(ctx context.Context, in transition.In, p progress) (transition.Result, error) {
	n := in.Job.Subject.Number
	marker := owed.RevisionReplyMarker(n, p.Pushed)
	item := owed.Comment(fmt.Sprintf("revise-reply-pr-%d-%s", n, p.Nonce), n, marker, d.replyBody(n, p))
	return d.book().Owe(ctx, in, Replying, owed.Record{Next: Reviewing, Due: true, Items: []owed.Item{item}})
}

// replied is `revise-replied`: on to the review once the reply is on the pull
// request.
//
// A record lost with the state directory looks for the reply by its marker
// first. One that is there goes on to the review, as the record would have,
// and the review hands back linking it if someone else has pushed since: the
// watch would hand back on their push without the link, saying the reply's
// points again. One that is not there goes back to the watch, which owes the
// reply again if CI is still green on the head, and hands the revision back if
// its progress went too.
func (d *Deps) replied(ctx context.Context, in transition.In) (transition.Result, error) {
	res, err := d.book().Settle(ctx, in, transition.Result{State: Watching, RunAt: in.Now})
	if err != nil || res.State != Watching {
		return res, err
	}
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return transition.Result{}, err
	}
	n := in.Job.Subject.Number
	comments, err := d.Tracker.Comments(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	if _, ok := d.replyOf(comments, n, p); ok {
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	return res, nil
}

// replyOf is the revision's reply among comments: the agent's, carrying the
// marker for the head it pushed.
func (d *Deps) replyOf(comments []github.Comment, n int, p progress) (github.Comment, bool) {
	marker := owed.RevisionReplyMarker(n, p.Pushed)
	for _, c := range comments {
		if strings.EqualFold(c.Login, d.Login) && strings.Contains(c.Body, marker) {
			return c, true
		}
	}
	return github.Comment{}, false
}

// replyBody is the revision's reply: what Go knows, then what the session
// said.
//
// Go's part is a compare link from the head the operator read to the head the
// revision left, and a line when the pull request is now over the size signal.
// The session's part is its sections from the reply file, in order, with empty
// ones left out. There is no narration of what changed, no "tests pass", and
// no self-rating: "done" is the session's own report, so the links that make
// it cheap to check are the point.
func (d *Deps) replyBody(n int, p progress) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s", owed.RevisionReplyMarker(n, p.Pushed), revisionMarkers(p.Points))
	fmt.Fprintf(&b, "[Changes since your review](https://github.com/%s/compare/%s...%s): `%s` to `%s`.\n", d.Repo, p.Read, p.Pushed, git.Short(p.Read), git.Short(p.Pushed))
	if c := (size.Count{Lines: p.Lines, Tests: p.Tests}); p.Measured && c.Over(d.SizeSignal) {
		fmt.Fprintf(&b, "\nThe pull request is now %d changed lines, and %d changed lines of tests, which is over the size signal of %d.\n", c.Lines, c.Tests, d.SizeSignal)
	}
	if s := Sections(p.Reply); s != "" {
		fmt.Fprintf(&b, "\n%s\n", s)
	}
	return b.String()
}

// hidden is an HTML comment: how every marker the agent reads back is written.
var hidden = regexp.MustCompile(`(?s)<!--.*?-->`)

// Sections is the session's part of the reply: the sections it wrote under
// the headings the prompt names, in that order, with empty ones and anything
// else left out. A heading written twice is one section.
//
// It carries no hidden line. The reply is a comment the agent wrote, so a
// marker the session typed would be read back as the agent's own: a review of
// a head, say, that was never written. An opening left unclosed is shown
// rather than hide the rest.
func Sections(text string) string {
	text = strings.ReplaceAll(hidden.ReplaceAllString(text, ""), "<!--", "&lt;!--")
	bodies := map[string]*strings.Builder{}
	var at *strings.Builder
	fence := false
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
		}
		if h, ok := strings.CutPrefix(line, "## "); ok && !fence {
			at = nil
			for _, name := range sections {
				if strings.EqualFold(strings.TrimSpace(h), name) {
					if bodies[name] == nil {
						bodies[name] = &strings.Builder{}
					}
					at = bodies[name]
				}
			}
			continue
		}
		if at != nil {
			fmt.Fprintf(at, "%s\n", line)
		}
	}
	var out []string
	for _, name := range sections {
		if body := bodies[name]; body != nil {
			if s := strings.TrimSpace(body.String()); s != "" {
				out = append(out, fmt.Sprintf("## %s\n\n%s", name, s))
			}
		}
	}
	return strings.Join(out, "\n\n")
}

// awaitReview is `revise-review`: ask for the advisory review of the green
// head, wait for it, and hand the pull request off once it is there (package
// handoff). The review job claims the reply this job posted as its request.
func (d *Deps) awaitReview(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	if pr.State != "open" {
		// Closed, or merged, by a human while the review was coming.
		return d.rest(in.Job.ID)
	}
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.handBackLost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	r, err := d.handOffDeps().AwaitReview(ctx, in, n, p.Progress)
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case handoff.Done:
		return transition.Result{State: HandingOff, RunAt: in.Now}, nil
	case handoff.HandedBack:
		return d.rest(in.Job.ID)
	case handoff.HandBack:
		return d.handBackReplied(ctx, in, p, r.Reason, r.Output)
	}
	return transition.Result{State: Reviewing, RunAt: r.RunAt, Effects: r.Effects}, nil
}

// handOff is `revise-hand-off`: the hand-off label back on the pull request,
// read back from the tracker (package handoff). There is no round cap on
// revisions: the next send-back is a new one.
func (d *Deps) handOff(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	if pr.State != "open" {
		return d.rest(in.Job.ID)
	}
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.handBackLost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	r, err := d.handOffDeps().HandOff(ctx, in, pr, p.Pushed, fmt.Sprintf("revise-hand-off-pr-%d-%s", n, p.Pushed))
	if err != nil {
		return transition.Result{}, err
	}
	switch r.State {
	case handoff.Done:
		return d.rest(in.Job.ID)
	case handoff.HandBack:
		return d.handBackReplied(ctx, in, p, r.Reason, r.Output)
	}
	return transition.Result{State: HandingOff, RunAt: r.RunAt, Effects: r.Effects}, nil
}

// handBackReplied is a hand-back once the reply is posted. The reply stays as
// it is, since it is still true about the change, and the hand-back links it
// rather than say the points again.
func (d *Deps) handBackReplied(ctx context.Context, in transition.In, p progress, reason, output string) (transition.Result, error) {
	n := in.Job.Subject.Number
	comments, err := d.Tracker.Comments(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	detail := "What the revision did is in its reply above."
	if c, ok := d.replyOf(comments, n, p); ok {
		detail = fmt.Sprintf("What the revision did is in [its reply](https://github.com/%s/pull/%d#issuecomment-%d).", d.Repo, n, c.ID)
	}
	return d.handBackOn(ctx, in, p, p.Nonce, reason, detail, output)
}

// rest is the revision at rest: its workspace, progress and send-back are done
// with.
func (d *Deps) rest(jobID string) (transition.Result, error) {
	return transition.Result{State: Start}, errors.Join(d.work().Clear(jobID), d.clear(jobID))
}

// handOffDeps is the hand-off's view of this kind, and its bounds:
// implement's, since one review job serves both kinds.
func (d *Deps) handOffDeps() handoff.Deps {
	return handoff.Deps{Work: d.work(), Tracker: d.Tracker, Store: d.Store, Login: d.Login, Ask: d.AskReview, Label: d.HandOffLabel, Wait: d.CIWait, Rounds: d.Rounds}
}
