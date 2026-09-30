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
//	reviewing --revise-review--->  handing-off  the review of the pushed head is on the pull request
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
// while a revision was in flight, and a command with no points. A closed pull
// request's commands are claimed and nothing more.
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
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/statefile"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The revise kind's states.
const (
	Start       = "start"
	Claiming    = "claiming"
	Revising    = "revising"
	Gating      = "gating"
	Pushing     = "pushing"
	Pushed      = "pushed"
	Replaying   = "replaying"
	Watching    = "watching"
	Replying    = "replying"
	Reviewing   = "reviewing"
	HandingOff  = "handing-off"
	Deferred    = "deferred"
	HandingBack = "handing-back"
)

// Word is the command that sends a pull request back to be revised.
const Word = "/revise"

// Tracker is what the revise kind reads and writes. *github.Client is one.
type Tracker interface {
	owed.Tracker
	PullRequest(ctx context.Context, number int) (github.PullRequest, error)
	CheckRuns(ctx context.Context, sha string) ([]github.CheckRun, error)
	RequiredChecks(ctx context.Context, branch string) ([]string, error)
	EditPullRequest(ctx context.Context, number int, body string) error

	// Reviews, ReviewComments and owed.ReviewTracker are how a send-back
	// issued as a submitted review is read and claimed (#133).
	Reviews(ctx context.Context, number int) ([]github.Review, error)
	ReviewComments(ctx context.Context, number int, review int64) ([]github.ReviewComment, error)
	owed.ReviewTracker
}

// Model runs one model. opencode.Command is one.
type Model interface {
	Run(ctx context.Context, req opencode.Request) (opencode.Reply, error)
}

// Deps is everything the revise kind's transitions reach. Built once, by the
// command surface; the transitions themselves hold nothing.
type Deps struct {
	Tracker Tracker
	Model   Model

	// Store is read, never written: which round of an effect is next. This
	// job's own state is the runner's to write.
	Store store.Store

	// Login is the agent's own account: whose reaction is a claim, and whose
	// comment is an answer.
	Login string

	// Repo is the repository, as owner/name. A pull request whose branch is
	// in any other - a fork's, or one deleted - is one the agent cannot push
	// to.
	Repo string

	// Remote is the repository a revision is cloned from and pushed to, and
	// the App's installation token every git process that reaches it carries.
	Remote git.Remote

	// Resolve is the ordered candidate list a revision runs on, as of now
	// (ADR 0001 §9): the implement tier's, since a revision is implementing
	// work on a branch. A *model.LimitedError defers the job to the reset.
	Resolve func(ctx context.Context) (model.Candidates, error)

	// Bound is how many candidates a run tries before the tier counts as
	// exhausted, and TierWait how long an exhausted tier defers. Parameters.
	Bound    int
	TierWait time.Duration

	// Rounds is how many times something owed is made before one that never
	// appears is an error. A parameter.
	Rounds int

	// Gate is the local gate: a shell command run in the workspace, which
	// passes by exiting zero, and Attempts how many times it may fail before
	// the revision is handed back. Parameters.
	Gate     string
	Attempts int

	// CIWait is how long a head whose checks are not finished waits before
	// it is looked at again, CICeiling how long after its push they may
	// take before the revision is handed back, and CIFixes how many times a
	// red run is sent back to the session. Parameters.
	CIWait    time.Duration
	CICeiling time.Duration
	CIFixes   int

	// Denylist is the paths the agent may never push, as globs (see work).
	// A parameter.
	Denylist []string

	// Replays is how many times a revision is replayed onto someone else's
	// push before one more is handed back. A parameter.
	Replays int

	// Sensitive is the paths the operator named as deserving closer reading,
	// which the description's sensitive line names when the pull request
	// touches one. Empty is the feature off. A parameter.
	Sensitive []sensitive.Path

	// HandOffLabel is the label the hand-off applies, which a claim that
	// moves on to the work takes off and the revision's hand-off puts back.
	// A parameter.
	HandOffLabel string

	// SizeSignal is the changed non-test lines a pull request may have
	// before the reply says it is over. A note, never a cut or a
	// hand-back. A parameter.
	SizeSignal int

	// AskReview makes the pull request's review job due now, under a lease
	// of its own (handoff.Asker): how the revision asks for the advisory
	// review of its green head.
	AskReview func(ctx context.Context, pr store.Subject, now time.Time) error

	// HandBackLabel is the label a hand-back applies. A parameter.
	HandBackLabel string

	// StateDir is where what is owed, the send-back and the revision's
	// workspace wait - beside the store, never in it (ADR 0001 §5).
	StateDir string

	// Log receives one line each time a candidate's run fails transiently,
	// which nothing else keeps once the next candidate runs, and one each
	// time CI fails a head the local gate passed. Nil is silent.
	Log func(msg string)
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
		{Name: "revise-gate", Kind: store.KindRevise, From: Gating, Tokens: []string{transition.HeavyBuild}, Run: d.gate},
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
	// Comment is the comment command the point is in, or Review the review
	// command: which the revision's reply answers.
	Comment int64 `json:"comment,omitempty"`
	Review  int64 `json:"review,omitempty"`

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
	reviews, err := d.Tracker.Reviews(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	book := d.book()
	unansweredComments, err := book.Unanswered(ctx, comments, Word)
	if err != nil {
		return transition.Result{}, err
	}
	unansweredReviews, err := book.UnansweredReviews(ctx, reviews, Word)
	if err != nil {
		return transition.Result{}, err
	}
	conversation := timeline(comments, reviews)
	commands := timeline(unansweredComments, unansweredReviews)

	var items []owed.Item
	for _, c := range commands {
		items = append(items, c.claim())
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
			items = append(items, c.refuse("revise-unpushable-"+c.key(), n,
				fmt.Sprintf("This pull request's branch is not in %s, so the agent cannot push to it. Nothing was done.", d.Repo)))
			continue
		}
		if d.inFlight(conversation, c) {
			items = append(items, c.refuse("revise-in-flight-"+c.key(), n,
				fmt.Sprintf("A revision of this pull request was in flight when this was written, so the head it was written against is no longer the pull request's. Nothing was done: read what the revision changed, then `%s` again.", Word)))
			continue
		}
		pts, err := d.points(ctx, n, c)
		if err != nil {
			return transition.Result{}, err
		}
		if len(pts) == 0 {
			items = append(items, c.refuse("revise-no-points-"+c.key(), n, c.noPoints()))
			continue
		}
		if first == "" {
			first = c.key()
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
	return &owed.Book{Tracker: d.Tracker, Store: d.Store, Login: d.Login, Rounds: d.Rounds, Dir: filepath.Join(d.StateDir, "owed"), Reviews: d.Tracker}
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
func (d *Deps) inFlight(conversation []command, c command) bool {
	at := -1
	for i, e := range conversation {
		if e.same(c) {
			at = i
		}
	}
	for _, earlier := range conversation[:max(at, 0)] {
		if !earlier.issues(d.Login) {
			continue
		}
		marker := earlier.revisionMarker()
		for _, e := range conversation[at+1:] {
			if !e.isReview() && strings.EqualFold(e.comment.Login, d.Login) && strings.Contains(e.comment.Body, marker) {
				return true
			}
		}
	}
	return false
}

// command is one `/revise`: a comment on the pull request's conversation, or a
// submitted review whose body starts with the word (#133). The two are claimed,
// refused and answered the same way, each on its own id.
type command struct {
	comment github.Comment

	// review is the review, when the command is one. Its ID is zero for a
	// comment.
	review github.Review
}

func (c command) isReview() bool { return c.review.ID != 0 }

// same reports whether c and o are the same comment, or the same review.
func (c command) same(o command) bool {
	if c.isReview() {
		return o.isReview() && o.review.ID == c.review.ID
	}
	return !o.isReview() && o.comment.ID == c.comment.ID
}

// key is what the command's owed items are keyed on.
func (c command) key() string {
	if c.isReview() {
		return fmt.Sprintf("review-%d", c.review.ID)
	}
	return fmt.Sprintf("comment-%d", c.comment.ID)
}

// issues reports whether c is a `/revise` from a writer who is not login.
func (c command) issues(login string) bool {
	if c.isReview() {
		return intake.IsReviewCommand(c.review, login, Word)
	}
	return intake.IsCommand(c.comment, login, Word)
}

// claim is the agent's 👀 on the command: on the comment, or on the review.
func (c command) claim() owed.Item {
	if c.isReview() {
		return owed.ClaimReview(c.review)
	}
	return owed.Claim(c.comment)
}

// refuse is the one reply saying why the command cannot be revised. One to a
// review links it: the reply is in the conversation, and the review is not.
func (c command) refuse(stem string, n int, text string) owed.Item {
	if c.isReview() {
		return owed.ReplyToReview(stem, n, c.review, fmt.Sprintf("On [your review](%s): %s", c.review.URL, text))
	}
	return owed.Reply(stem, n, c.comment, text)
}

// noPoints is the refusal of a command with no points.
func (c command) noPoints() string {
	if c.isReview() {
		return fmt.Sprintf("There is nothing here to revise. Write the points after `%s` in the review's body, or as line comments in the same review. Nothing was done.", Word)
	}
	return fmt.Sprintf("There is nothing here to revise. Write the points after `%s`, in the same comment. Nothing was done.", Word)
}

// revisionMarker is the hidden line a revision's answer to c carries.
func (c command) revisionMarker() string {
	if c.isReview() {
		return owed.ReviewRevisionMarker(c.review.ID)
	}
	return owed.RevisionMarker(c.comment.ID)
}

// timeline is comments and the submitted reviews among reviews, in the order
// they were written. The conversation's listing leaves reviews out, so each
// review goes after every comment written no later than it was submitted,
// and the comments keep the order the listing gave them. A comment written in
// the same second as a review, which is the timestamps' resolution, goes
// before it.
func timeline(comments []github.Comment, reviews []github.Review) []command {
	var submitted []github.Review
	for _, r := range reviews {
		if r.State != "PENDING" {
			submitted = append(submitted, r)
		}
	}
	sort.SliceStable(submitted, func(i, j int) bool { return submitted[i].SubmittedAt.Before(submitted[j].SubmittedAt) })
	out := make([]command, 0, len(comments)+len(submitted))
	i := 0
	for _, r := range submitted {
		for ; i < len(comments) && !comments[i].CreatedAt.After(r.SubmittedAt); i++ {
			out = append(out, command{comment: comments[i]})
		}
		out = append(out, command{review: r})
	}
	for ; i < len(comments); i++ {
		out = append(out, command{comment: comments[i]})
	}
	return out
}

// points is command c's points: a comment's body after the word, or a
// review's body after the word and then each of its line comments, in order.
// A line comment outside the review is never one of them.
func (d *Deps) points(ctx context.Context, n int, c command) ([]Point, error) {
	if !c.isReview() {
		if text := Points(c.comment.Body); text != "" {
			return []Point{{Comment: c.comment.ID, Text: text}}, nil
		}
		return nil, nil
	}
	var out []Point
	if text := Points(c.review.Body); text != "" {
		out = append(out, Point{Review: c.review.ID, Text: text})
	}
	lines, err := d.Tracker.ReviewComments(ctx, n, c.review.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		if text := strings.TrimSpace(l.Body); text != "" {
			out = append(out, Point{Review: c.review.ID, Text: text, Path: l.Path, Line: l.Line, URL: l.URL})
		}
	}
	return out, nil
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
