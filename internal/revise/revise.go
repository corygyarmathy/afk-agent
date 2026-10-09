// Package revise is the revise job kind's transitions: a send-back on a pull
// request becomes a revision of it (#131).
//
// A send-back's whole way to a revision (#145, #146, #134, #147):
//
//	start     --revise---------->  claiming   claim every unanswered command, and refuse what cannot be revised
//	claiming  --revise-claimed-->  revising   the claims, the replies and the hand-off label taken off are on the tracker
//	                               start      ... and there is nothing to revise: at rest
//	                               claiming   made again, under the next key
//	revising  --revise-run------>  gating     one candidate model, in the workspace, on the send-back's head
//	                               revising   it failed transiently: the next candidate
//	                               handing-back  the branch was deleted, or pushed over, since the send-back,
//	                                             or the workspace was lost after the revision pushed
//	gating    --revise-gate----->  pushing    the local gate passed
//	                               revising   it failed: back to the session that wrote it
//	                               handing-back  out of attempts, or the session rewrote the read head
//	pushing   --revise-push----->  pushed     the denylist, the size and the sensitive paths, and the leased push
//	                               pushing    the push did not land: again, under the next key
//	                               handing-back  a denied path, the read head rewritten, or out of rounds
//	pushed    --revise-pushed--->  watching   the push is on the remote, and the sensitive line right or given up on: CI is #147's
//	                               pushed     the sensitive line edited: read back
//	                               pushing    not landed yet: again
//	                               replaying  someone else pushed during the revision
//	                               handing-back  the branch was deleted during the revision
//	replaying --revise-replay--->  gating     the revision's own commits, on top of their push
//	                               pushed     the revision's push had landed, and theirs is on top of it
//	                               handing-back  their push dropped what was read, a replay conflicts, or out of replays
//	watching  --revise-watch---->  replying   CI is green on the pushed head: the reply, owed
//	                               watching   not finished: again after the CI wait
//	                               revising   red: back to the revision's session, with what CI said
//	                               handing-back  out of fixes, past the ceiling, or someone else pushed
//	                               start      the pull request was closed: at rest
//	replying  --revise-replied-->  reviewing  the reply is on the pull request
//	                               replying   made again, under the next key
//	                               watching   its record was lost: CI is looked at again
//	reviewing --revise-review--->  handing-off  the review of the pushed head is on the pull request, with nothing to correct
//	                               revising   on the agent's own pull request, its Correctness or Standards findings:
//	                                          back to the session, once, to be corrected (then gating, pushing, pushed
//	                                          and watching as for any push, never replaying, and a failure of the
//	                                          correction's own goes back to the head the review read and on here)
//	                               reviewing  a correction green or failed: the review edited to say so, under the next key
//	                               handing-off  ... and read back, or given up on
//	                               reviewing  the review job made due, or still on its way
//	                               start      the review job handed its review back: at rest
//	                               handing-back  someone else pushed, the review job failed, or out of rounds
//	handing-off --revise-hand-off--> start    the hand-off label is on the pull request: at rest
//	                               handing-off  applied under the next key
//	                               handing-back  out of rounds
//	handing-back --revise-handed-back--> start  the hand-back's comment and label are on the tracker: at rest
//	deferred  --revise-resume----> revising   the tier again, from its first model
//
// The revision's own commits go on top of the head the send-back was written
// against, and are pushed under a lease pinned to that head: a push made while
// the revision ran is never overwritten (#146). When the lease refuses because
// someone else pushed, the revision's own commits are replayed onto their push,
// gated again and pushed under a lease pinned to it, a bounded number of times
// (#134).
//
// Each push is measured as the pull request will show it after the revision:
// the whole of it, against its base branch's current tip, which a rebase the
// operator made before sending it back has moved (#148). The description's
// sensitive line is brought to what it touches, and the size kept for the
// reply. A measure that fails is logged, and never holds back the push. The
// rest of the description is never rewritten. The workspace, the relay, the
// gate and its retries, the denylist, the leased push and the replay are
// package work, shared with implement.
//
// A revision ends as implement's work does, in this order: the reply, the
// advisory review, and the hand-off (#149). The reply answers every command of
// the send-back in one comment, once CI is green on the revision's final head,
// so everything it links is what the operator will read: the changes since
// the head they read, a line when the pull request is over the size signal,
// and the session's own points, follow-ups and what it did not verify. It is
// the revision's orientation, and is read before the review. The review is the
// review job made due for the new head, which claims the reply with a 👀 and
// links it. The hand-off label goes back on once the review is there. Anything
// that stops the revision after the reply is posted hands back, and leaves the
// reply as it is: it is still true about the change.
//
// The claim is its own transition for the reason implement's and review's are:
// it is committed before anything can fail. A job that failed ahead of its
// claim would come to rest with its command unanswered, and intake arms a
// command only once. It is read back before the job moves on (package owed),
// and so is the hand-off label it takes off: that label is how the review
// queue counts a pull request waiting on the operator, and while its revision
// is in flight the revision counts in its place. A claim that only refuses
// leaves the label where it was: nothing is being revised, so the pull request
// still waits on the operator's review, not the agent.
//
// A command is a comment starting `/revise`, or a submitted review whose body
// does (#133). A send-back's points are the command's body after the word, in
// the operator's own words, and a review's line comments after its body, each
// with where it is. What the review says - approve, request changes, comment -
// decides nothing, and a line comment outside it is never a point. A review is
// claimed with a 👀 on the review itself, keyed on its own id. Every unanswered
// `/revise` with points, of either form, is part of the one send-back, in the
// order they were written: nothing was pushed between them, so they were all
// written against the same head. What cannot be revised is refused with one
// reply, and nothing else: a branch the agent cannot push to, a command written
// while a revision was in flight, a review written on a head the pull request
// has since left, and a command with no points. A closed pull request's
// commands are claimed and nothing more.
//
// Whether a command was written while a revision was in flight is read from the
// tracker, by where the comments are in the conversation, with each review
// placed among them by the time it was submitted. This job's claim cannot run
// during a flight - intake does not arm a job that is queued or held - so a
// command written then is read afterwards, and its head has moved on under it.
// It was written during one if an earlier `/revise`'s answer, the agent's
// comment carrying the revision's marker for it, comes after it: the revision's
// reply and its hand-back carry that marker for every command of the send-back.
// A refusal carries a reply marker instead, so it does not read as a revision.
// A revision that ended without either, by parking, blocks nothing after it.
package revise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/delivery"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The revise kind's states. Those a delivery shares are delivery's.
const (
	Start       = delivery.Start
	Claiming    = "claiming"
	Revising    = "revising"
	Gating      = delivery.Gating
	Pushing     = delivery.Pushing
	Pushed      = "pushed"
	Replaying   = "replaying"
	Watching    = delivery.Watching
	Replying    = "replying"
	Reviewing   = delivery.Reviewing
	HandingOff  = delivery.HandingOff
	Deferred    = delivery.Deferred
	HandingBack = delivery.HandingBack
)

// States is every state a revise job can be in, each of which a transition
// runs from.
func States() []string {
	return []string{Start, Claiming, Revising, Gating, Pushing, Pushed, Replaying, Watching, Replying, Reviewing, HandingOff, Deferred, HandingBack}
}

// Word is the command that sends a pull request back to be revised.
const Word = "/revise"

// Tracker is what the revise kind reads and writes. *github.Client is one.
type Tracker interface {
	owed.Tracker
	PullRequest(ctx context.Context, number int) (github.PullRequest, error)
	CheckRuns(ctx context.Context, sha string) ([]github.CheckRun, error)
	RequiredChecks(ctx context.Context, branch string) ([]string, error)
	EditPullRequest(ctx context.Context, number int, body string) error

	// PullRequestReviews, LineComments and owed.PullRequestReviewTracker are
	// how a send-back issued as a submitted review is read and claimed
	// (#133).
	PullRequestReviews(ctx context.Context, number int) ([]github.PullRequestReview, error)
	LineComments(ctx context.Context, number int, review int64) ([]github.LineComment, error)
	owed.PullRequestReviewTracker

	// EditComment is how the advisory review is edited once a correction of
	// its findings is done with (package correction).
	EditComment(ctx context.Context, commentID int64, body string) error
}

// Deps is everything the revise kind's transitions reach. Built once, by the
// command surface; the transitions themselves hold nothing.
//
// Of the parameters it shares with implement, what is this kind's to say: a
// pull request whose branch is in any repository but Repo - a fork's, or one
// deleted - is one the agent cannot push to. HandOffLabel is taken off by a
// claim that moves on to the work, and put back by the revision's hand-off.
// Over SizeSignal is a note in the reply, never a cut or a hand-back.
type Deps struct {
	delivery.Params

	Tracker Tracker

	// Replays is how many times a revision is replayed onto someone else's
	// push before one more is handed back. A parameter.
	Replays int
}

// logf is one line to Log, if there is one.
func (d *Deps) logf(format string, a ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(format, a...))
	}
}

// Transitions is the revise kind, as registry entries.
func Transitions(d *Deps) []transition.Transition {
	return []transition.Transition{
		{Name: "revise", Kind: store.KindRevise, From: Start, Run: d.claim},
		{Name: "revise-claimed", Kind: store.KindRevise, From: Claiming, Run: d.claimed},
		{Name: "revise-run", Kind: store.KindRevise, From: Revising, Tokens: []string{transition.HeavyBuild}, Run: d.run},
		delivery.Must(d.machine().Gate("revise-gate")),
		{Name: "revise-push", Kind: store.KindRevise, From: Pushing, Run: d.push},
		{Name: "revise-pushed", Kind: store.KindRevise, From: Pushed, Run: d.pushed},
		{Name: "revise-watch", Kind: store.KindRevise, From: Watching, Run: d.watch},
		{Name: "revise-replay", Kind: store.KindRevise, From: Replaying, Run: d.replay},
		{Name: "revise-replied", Kind: store.KindRevise, From: Replying, Run: d.replied},
		{Name: "revise-review", Kind: store.KindRevise, From: Reviewing, Run: d.awaitReview},
		{Name: "revise-hand-off", Kind: store.KindRevise, From: HandingOff, Run: d.handOff},
		{Name: "revise-handed-back", Kind: store.KindRevise, From: HandingBack, Run: d.handedBack},
		{Name: "revise-resume", Kind: store.KindRevise, From: Deferred, Run: d.resume},
	}
}

// SendBack is what a claim that moves on to the work hands it: the branch and
// head the send-back was written against, and its points.
type SendBack struct {
	Head   string  `json:"head"`
	Ref    string  `json:"ref"`
	Points []Point `json:"points"`

	// Pushed is the revision's last push seen on the remote, once it has
	// one. The progress has it too, as the lease, but the progress can be
	// lost without the send-back: this is what tells a revision that pushed
	// from one that never did.
	Pushed string `json:"pushed,omitempty"`
}

// Point is one of a send-back's points, as the operator wrote it: a comment
// command's body, a review command's body, or one of that review's line
// comments.
type Point struct {
	// Comment is the comment command the point is in, or PullRequestReview
	// the review command: which the revision's reply answers.
	Comment           int64 `json:"comment,omitempty"`
	PullRequestReview int64 `json:"pull_request_review,omitempty"`

	Text string `json:"text"`

	// Path and Line are where a line comment is, and URL the line comment
	// itself, which the reply identifies the point by. Line is zero for a
	// comment on a whole file. All three are empty for a command's body.
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	URL  string `json:"url,omitempty"`
}

// claim is `revise`: take every unanswered command, refuse what cannot be
// revised, and move on to the work with the rest.
func (d *Deps) claim(ctx context.Context, in transition.In) (transition.Result, error) {
	if d.Repo == "" {
		// Without it no branch would be one the agent can push to.
		return transition.Result{}, errors.New("revise does not know its repository")
	}
	n := in.Job.Subject.Number
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	comments, err := d.Tracker.Comments(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	reviews, err := d.Tracker.PullRequestReviews(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	book := d.book()
	conversation := timeline(comments, reviews)
	commands, err := book.UnansweredCommands(ctx, conversation, Word)
	if err != nil {
		return transition.Result{}, err
	}

	var items []owed.Item
	for _, c := range commands {
		items = append(items, c.Claim())
	}
	// Whatever an earlier claim handed the work is done with or given up
	// on, and this is a fresh send-back or none.
	if err := d.clear(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	if pr.State != "open" {
		// Nothing to revise, and nothing to say: the claims are enough to
		// stop the commands being armed again.
		return book.Owe(ctx, in, Claiming, owed.Record{Next: Start, Items: items})
	}

	var (
		points []Point
		first  string
	)
	for _, c := range commands {
		if !strings.EqualFold(pr.HeadRepo, d.Repo) {
			items = append(items, c.Reply("revise-unpushable-"+c.Key(), n,
				fmt.Sprintf("This pull request's branch is not in %s, so the agent cannot push to it. Nothing was done.", d.Repo)))
			continue
		}
		if d.inFlight(conversation, c) {
			items = append(items, c.Reply("revise-in-flight-"+c.Key(), n,
				fmt.Sprintf("A revision of this pull request was in flight when this was written, so the head it was written against is no longer the pull request's. Nothing was done: read what the revision changed, then `%s` again.", Word)))
			continue
		}
		pts, on, err := d.points(ctx, n, c, pr.HeadSHA)
		if err != nil {
			return transition.Result{}, err
		}
		if on != pr.HeadSHA {
			items = append(items, c.Reply("revise-moved-"+c.Key(), n,
				fmt.Sprintf("This review was written against `%s`, and the pull request's head is now `%s`, so what it points at may have moved. Nothing was done: read the pull request at its head, then `%s` again.", git.Short(on), git.Short(pr.HeadSHA), Word)))
			continue
		}
		if len(pts) == 0 {
			items = append(items, c.Reply("revise-no-points-"+c.Key(), n, noPoints(c)))
			continue
		}
		if first == "" {
			first = c.Key()
		}
		points = append(points, pts...)
	}
	if len(points) == 0 {
		return book.Owe(ctx, in, Claiming, owed.Record{Next: Start, Items: items})
	}

	// Owed even if the label is not there now: the read-back is what catches
	// one applied after the pull request above was read but before the job
	// moves on, and taking off a label that is not there is not an error.
	items = append(items, owed.Unlabel("revise-unlabel-"+first, n, d.HandOffLabel))
	if err := statefile.Save(d.path(in.Job.ID), SendBack{Head: pr.HeadSHA, Ref: pr.HeadRef, Points: points}); err != nil {
		return transition.Result{}, err
	}
	return book.Owe(ctx, in, Claiming, owed.Record{Next: Revising, Due: true, Items: items})
}

// claimed is `revise-claimed`: on to what the claim decided, once what it owes
// is on the tracker. A record lost with the state directory sends the job back
// to claim, which reads the commands afresh: a send-back whose claims had
// landed is lost with it, as the send-back it saved beside it would be.
func (d *Deps) claimed(ctx context.Context, in transition.In) (transition.Result, error) {
	return d.book().Settle(ctx, in, transition.Result{State: Start, RunAt: in.Now})
}

// book is the revise kind's way to what it owes the tracker.
func (d *Deps) book() *owed.Book {
	return &owed.Book{Tracker: d.Tracker, Store: d.Store, Login: d.Login, Rounds: d.Rounds, Dir: filepath.Join(d.StateDir, "owed"), PullRequestReviews: d.Tracker}
}

// inFlight reports whether command c was written while a revision was in
// flight: an earlier `/revise` was answered as a revision after it, in the
// conversation as timeline puts it.
//
// The revision's reply and its hand-back both carry the revision's marker for
// every command of the send-back, so either one coming after c means c was
// written against a head the revision has since moved. A refusal's reply
// carries a reply marker instead, so a command written between a refusal and
// its reply landing does not read as in flight: nothing was being revised.
func (d *Deps) inFlight(conversation []owed.Command, c owed.Command) bool {
	at := -1
	for i, e := range conversation {
		if e.Same(c) {
			at = i
		}
	}
	for _, earlier := range conversation[:max(at, 0)] {
		if !earlier.Issues(d.Login, Word) {
			continue
		}
		marker := earlier.RevisionMarker()
		for _, e := range conversation[at+1:] {
			if e.Comment != nil && strings.EqualFold(e.Comment.Login, d.Login) && strings.Contains(e.Comment.Body, marker) {
				return true
			}
		}
	}
	return false
}

// noPoints is the refusal of a command with no points.
func noPoints(c owed.Command) string {
	if c.PullRequestReview != nil {
		return fmt.Sprintf("There is nothing here to revise. Write the points after `%s` in the review's body, or as line comments in the same review. Nothing was done.", Word)
	}
	return fmt.Sprintf("There is nothing here to revise. Write the points after `%s`, in the same comment. Nothing was done.", Word)
}

// timeline is comments and the submitted reviews among reviews, in the order
// they were written: each review after every comment written no later than it
// was submitted, and the comments in the order the listing gave them. A
// comment written in the same second as a review, which is the timestamps'
// resolution, goes before it.
func timeline(comments []github.Comment, reviews []github.PullRequestReview) []owed.Command {
	var submitted []github.PullRequestReview
	for _, r := range reviews {
		if r.State != "PENDING" {
			submitted = append(submitted, r)
		}
	}
	sort.SliceStable(submitted, func(i, j int) bool { return submitted[i].SubmittedAt.Before(submitted[j].SubmittedAt) })
	out := make([]owed.Command, 0, len(comments)+len(submitted))
	i := 0
	for j := range submitted {
		for ; i < len(comments) && !comments[i].CreatedAt.After(submitted[j].SubmittedAt); i++ {
			out = append(out, owed.Command{Comment: &comments[i]})
		}
		out = append(out, owed.Command{PullRequestReview: &submitted[j]})
	}
	for ; i < len(comments); i++ {
		out = append(out, owed.Command{Comment: &comments[i]})
	}
	return out
}

// points is command c's points, and the head it was written against: a
// comment's body after the word, or a review's body after the word and then
// each of its line comments, in order. A line comment outside the review is
// never one of them.
//
// A comment is taken to be written against the head the pull request has
// now: nothing it says is tied to one. A review names the commit it was
// written on, and so does each of its line comments, whose line is a line in
// that commit. One of them not on the head the revision starts from is
// reported as what the review was written against.
func (d *Deps) points(ctx context.Context, n int, c owed.Command, head string) ([]Point, string, error) {
	r := c.PullRequestReview
	if r == nil {
		if text := Points(c.Comment.Body); text != "" {
			return []Point{{Comment: c.Comment.ID, Text: text}}, head, nil
		}
		return nil, head, nil
	}
	if r.CommitID != head {
		return nil, r.CommitID, nil
	}
	var out []Point
	if text := Points(r.Body); text != "" {
		out = append(out, Point{PullRequestReview: r.ID, Text: text})
	}
	lines, err := d.Tracker.LineComments(ctx, n, r.ID)
	if err != nil {
		return nil, "", err
	}
	for _, l := range lines {
		if l.CommitID != head {
			return nil, l.CommitID, nil
		}
		if text := strings.TrimSpace(l.Body); text != "" {
			out = append(out, Point{PullRequestReview: r.ID, Text: text, Path: l.Path, Line: l.Line, URL: l.URL})
		}
	}
	return out, head, nil
}

// Points is a command's points: its body after the word, as written. Empty
// when there are none.
func Points(body string) string {
	rest, _ := strings.CutPrefix(strings.TrimSpace(body), Word)
	return strings.TrimSpace(rest)
}

// Load is the send-back the claim handed job's work, from the state directory.
func (d *Deps) Load(jobID string) (SendBack, error) {
	var sb SendBack
	if err := statefile.Load(d.path(jobID), &sb); err != nil {
		return SendBack{}, err
	}
	return sb, nil
}

// notePushed keeps head in job's send-back as the revision's push seen on the
// remote. A send-back that is gone has nothing to keep it in: revise-run hands
// back as lost without it.
func (d *Deps) notePushed(jobID, head string) error {
	sb, err := d.Load(jobID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || sb.Pushed == head {
		return err
	}
	sb.Pushed = head
	return statefile.Save(d.path(jobID), sb)
}

func (d *Deps) path(jobID string) string {
	return filepath.Join(d.StateDir, "send-backs", jobID+".json")
}

func (d *Deps) clear(jobID string) error {
	if err := os.Remove(d.path(jobID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
