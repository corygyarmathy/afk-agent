// Package revise is the revise job kind's transitions: a send-back on a pull
// request becomes a revision of it (#131).
//
// A send-back's whole way to a revision (#145, #146, #134):
//
//	start     --revise---------->  claiming   claim every unanswered command, and refuse what cannot be revised
//	claiming  --revise-claimed-->  revising   the claims, the replies and the hand-off label taken off are on the tracker
//	                               start      ... and there is nothing to revise: at rest
//	                               claiming   made again, under the next key
//	revising  --revise-run------>  gating     one candidate model, in the workspace, on the send-back's head
//	                               revising   it failed transiently: the next candidate
//	                               handing-back  the branch was deleted, or pushed over, since the send-back
//	gating    --revise-gate----->  pushing    the local gate passed
//	                               revising   it failed: back to the session that wrote it
//	                               handing-back  out of attempts, or the session rewrote the read head
//	pushing   --revise-push----->  pushed     the denylist, and the leased push
//	                               pushing    the push did not land: again, under the next key
//	                               handing-back  a denied path, the read head rewritten, or out of rounds
//	pushed    --revise-pushed--->  watching   the push is on the remote: CI is #147's
//	                               pushing    not landed yet: again
//	                               replaying  someone else pushed during the revision
//	                               handing-back  the branch was deleted during the revision
//	replaying --revise-replay--->  gating     the revision's own commits, on top of their push
//	                               handing-back  their push dropped what was read, a replay conflicts, or out of replays
//	handing-back --revise-handed-back--> start  the hand-back's comment and label are on the tracker: at rest
//	deferred  --revise-resume----> revising   the tier again, from its first model
//
// The revision's own commits go on top of the head the send-back was written
// against, and are pushed under a lease pinned to that head: a push made while
// the revision ran is never overwritten (#146). When the lease refuses because
// someone else pushed, the revision's own commits are replayed onto their push,
// gated again and pushed under a lease pinned to it, a bounded number of times
// (#134). The workspace, the relay, the gate and its retries, the denylist, the
// leased push and the replay are package work, shared with implement.
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
// A send-back's points are the command's body after the word, in the
// operator's own words. Every unanswered `/revise` with points is part of the
// one send-back, in the order they were written: nothing was pushed between
// them, so they were all written against the same head. What cannot be revised
// is refused with one reply, and nothing else: a branch the agent cannot push
// to, a command written while a revision was in flight, and a command with no
// points. A closed pull request's commands are claimed and nothing more.
//
// Whether a command was written while a revision was in flight is read from
// the tracker, by where the comments are in the conversation. This job's claim
// cannot run during a flight - intake does not arm a job that is queued or
// held - so a command written then is read afterwards, and its head has moved
// on under it. It was written during one if an earlier `/revise`'s answer, the
// agent's comment carrying owed.RevisionMarker for it, comes after it: the
// revision's reply and its hand-back carry that marker for every command of
// the send-back. A refusal carries owed.ReplyMarker instead, so it does not
// read as a revision. A revision that ended without either, by parking, blocks
// nothing after it.
package revise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
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
	Deferred    = "deferred"
	HandingBack = "handing-back"
)

// Word is the command that sends a pull request back to be revised.
const Word = "/revise"

// Tracker is what the revise kind reads and writes. *github.Client is one.
type Tracker interface {
	owed.Tracker
	PullRequest(ctx context.Context, number int) (github.PullRequest, error)
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

	// Denylist is the paths the agent may never push, as globs (see work).
	// A parameter.
	Denylist []string

	// Replays is how many times a revision is replayed onto someone else's
	// push before one more is handed back. A parameter.
	Replays int

	// HandOffLabel is the label the hand-off applies, which a claim that
	// moves on to the work takes off. A parameter.
	HandOffLabel string

	// HandBackLabel is the label a hand-back applies. A parameter.
	HandBackLabel string

	// StateDir is where what is owed, the send-back and the revision's
	// workspace wait - beside the store, never in it (ADR 0001 §5).
	StateDir string

	// Log receives one line each time a candidate's run fails transiently,
	// which nothing else keeps once the next candidate runs. Nil is silent.
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
		{Name: "revise-replay", Kind: store.KindRevise, From: Replaying, Run: d.replay},
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
}

// Point is one command's points, as the operator wrote them.
type Point struct {
	// Comment is the command the points are in, which the revision's reply
	// answers.
	Comment int64  `json:"comment"`
	Text    string `json:"text"`
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
	book := d.book()
	commands, err := book.Unanswered(ctx, comments, Word)
	if err != nil {
		return transition.Result{}, err
	}

	var items []owed.Item
	for _, c := range commands {
		items = append(items, owed.Claim(c))
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

	var points []Point
	for _, c := range commands {
		text := Points(c.Body)
		switch {
		case !strings.EqualFold(pr.HeadRepo, d.Repo):
			items = append(items, owed.Reply(fmt.Sprintf("revise-unpushable-comment-%d", c.ID), n, c,
				fmt.Sprintf("This pull request's branch is not in %s, so the agent cannot push to it. Nothing was done.", d.Repo)))
		case d.inFlight(comments, c.ID):
			items = append(items, owed.Reply(fmt.Sprintf("revise-in-flight-comment-%d", c.ID), n, c,
				fmt.Sprintf("A revision of this pull request was in flight when this was written, so the head it was written against is no longer the pull request's. Nothing was done: read what the revision changed, then `%s` again.", Word)))
		case text == "":
			items = append(items, owed.Reply(fmt.Sprintf("revise-no-points-comment-%d", c.ID), n, c,
				fmt.Sprintf("There is nothing here to revise. Write the points after `%s`, in the same comment. Nothing was done.", Word)))
		default:
			points = append(points, Point{Comment: c.ID, Text: text})
		}
	}
	if len(points) == 0 {
		return book.Owe(ctx, in, Claiming, owed.Record{Next: Start, Items: items})
	}

	// Owed even if the label is not there now: the read-back is what catches
	// one applied after the pull request above was read but before the job
	// moves on, and taking off a label that is not there is not an error.
	items = append(items, owed.Unlabel(fmt.Sprintf("revise-unlabel-comment-%d", points[0].Comment), n, d.HandOffLabel))
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
	return &owed.Book{Tracker: d.Tracker, Store: d.Store, Login: d.Login, Rounds: d.Rounds, Dir: filepath.Join(d.StateDir, "owed")}
}

// inFlight reports whether command id was written while a revision was in
// flight: an earlier `/revise` was answered as a revision after it.
//
// The revision's reply and its hand-back both carry owed.RevisionMarker for
// every command of the send-back, so either one coming after the command means
// that command was written against a head the revision has since moved. A
// refusal's reply carries owed.ReplyMarker instead, so a command written
// between a refusal and its reply landing does not read as in flight: nothing
// was being revised.
func (d *Deps) inFlight(comments []github.Comment, id int64) bool {
	at := -1
	for i, c := range comments {
		if c.ID == id {
			at = i
		}
	}
	for _, earlier := range comments[:max(at, 0)] {
		if !intake.IsCommand(earlier, d.Login, Word) {
			continue
		}
		marker := owed.RevisionMarker(earlier.ID)
		for i, c := range comments {
			if i > at && strings.EqualFold(c.Login, d.Login) && strings.Contains(c.Body, marker) {
				return true
			}
		}
	}
	return false
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

func (d *Deps) path(jobID string) string {
	return filepath.Join(d.StateDir, "send-backs", jobID+".json")
}

func (d *Deps) clear(jobID string) error {
	if err := os.Remove(d.path(jobID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
