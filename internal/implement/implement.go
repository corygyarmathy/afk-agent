// Package implement is the implement job kind's transitions: an issue becomes a
// pull request, for a human to review and merge (#40).
//
// An issue's whole way to a pull request handed off for review (#49-#53):
//
//	start        --implement-------------->  claiming      claim every unanswered command
//	claiming     --implement-claimed------>  implementing  the claims, and any reply, are on the tracker
//	                                         start         ... and a pull request is open already: at rest
//	                                         claiming      made again, under the next key
//	implementing --implement-run---------->  gating        one candidate model, in the workspace
//	gating       --implement-gate--------->  pushing       the local gate passed
//	                                         implementing  it failed: back to the session that wrote it
//	                                         handing-back  hand-back on the issue
//	pushing      --implement-push--------->  opening       the denylist, then the push
//	                                         handing-back  a denied path: hand-back on the issue
//	opening      --implement-open--------->  watching      the push is on the remote, and so is the pull request
//	                                         opening       the pull request, under the next key
//	                                         pushing       the push is not on the remote: again
//	watching     --implement-watch-------->  reviewing     CI is green on the pushed head
//	                                         watching      not finished: again after the CI wait
//	                                         implementing  red: back to the session, with what CI said
//	                                         handing-back  out of rounds, or past the ceiling: hand-back on the pull request
//	reviewing    --implement-review------->  handing-off   the review is on the pull request
//	                                         reviewing     the review job made due, or still on its way
//	handing-off  --implement-hand-off----->  start         the hand-off label is on the pull request: at rest
//	                                         handing-off   applied under the next key
//	handing-back --implement-handed-back-->  start         the hand-back's comment and label are on the tracker: at rest
//	                                         handing-back  whichever is not, made again under the next key
//	deferred     --implement-resume------->  implementing  the tier again, from its first model
//
// The claim is its own transition for the reason review's is: it is committed
// before anything can fail. A job that failed ahead of its claim would come to
// rest with its command unanswered, and intake arms a command only once.
//
// A claim and a hand-back are each read back from the tracker before the job
// moves on from them (package owed). The runner commits and then performs, so
// either could be lost with its key reserved: a claim lost that way leaves the
// command looking unanswered for ever, and a hand-back lost that way is a
// silent stop - the job is at rest, and it did not fail, so nobody is told.
//
// The gate is its own transition, and the agent's rather than the model's. The
// session is told to run it, and its word that it did is not the gate. Two
// counts bound the work, and they are kept apart: the job's stays move a
// model run that failed transiently to the next candidate, and the progress
// file beside the workspace counts the gate's failures. A candidate that fails
// transiently has not had a go at the work, and a gate failure is not a
// model's fault.
//
// Nothing here asks who made the job due. A command and, later, the unattended
// queue produce the same job (ADR 0001 §14), and `implement` claims whatever
// commands there are - none, for a job nobody commanded.
package implement

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The implement kind's states.
const (
	Start        = "start"
	Claiming     = "claiming"
	Implementing = "implementing"
	Gating       = "gating"
	Deferred     = "deferred"
	Pushing      = "pushing"
	Opening      = "opening"
	Watching     = "watching"
	Reviewing    = "reviewing"
	HandingOff   = "handing-off"
	HandingBack  = "handing-back"
)

// Word is the command that asks for an issue to be implemented.
const Word = "/implement"

// Tracker is what the implement kind reads and writes. *github.Client is one.
type Tracker interface {
	owed.Tracker
	OpenPullRequests(ctx context.Context) ([]github.PullRequest, error)
	CreatePullRequest(ctx context.Context, pr github.NewPullRequest) (github.PullRequest, error)
	CheckRuns(ctx context.Context, sha string) ([]github.CheckRun, error)
}

// Model runs one model. opencode.Command is one.
type Model interface {
	Run(ctx context.Context, req opencode.Request) (opencode.Reply, error)
}

// Deps is everything the implement kind's transitions reach. Built once, by
// the command surface; the transitions themselves hold nothing.
type Deps struct {
	Tracker Tracker
	Model   Model

	// Login is the agent's own account: whose reaction is a claim, and whose
	// pull requests are the agent's.
	Login string

	// BranchPrefix begins every branch the agent pushes for an issue. A
	// parameter.
	BranchPrefix string

	// Remote is the repository a workspace is cloned from: the tracker's
	// clone URL, or a local path in a test.
	Remote string

	// Resolve is the ordered candidate list for the implement tier, as of
	// now (ADR 0001 §9). A *model.LimitedError defers the job to the reset.
	Resolve func(ctx context.Context) (model.Candidates, error)

	// Bound is how many candidates a run tries before the tier counts as
	// exhausted, and TierWait how long an exhausted tier defers. Parameters.
	Bound    int
	TierWait time.Duration

	// Gate is the local gate: a shell command run in the workspace, which
	// passes by exiting zero. A parameter.
	Gate string

	// Attempts is how many times the gate may fail before the work is
	// handed back. A parameter.
	Attempts int

	// HandBackLabel is the label a hand-back applies, and HandOffLabel the
	// one the hand-off does. Parameters.
	HandBackLabel string
	HandOffLabel  string

	// CIWait is how long a head whose checks are not finished waits before
	// it is looked at again, CICeiling how long after its push they may
	// take before the work is handed back, and CIRounds how many times a
	// red run is sent back to the session. Parameters.
	CIWait    time.Duration
	CICeiling time.Duration
	CIRounds  int

	// Denylist is the paths the agent may never push, as globs (see
	// denied). A parameter.
	Denylist []string

	// Token is the credential a push carries: the App's installation
	// token. Nil pushes with none, which is a local remote in a test.
	Token func(ctx context.Context) (string, error)

	// Store is read, never written: which round of an effect is next, and
	// where the pull request's review job is. This job's own state is the
	// runner's to write.
	Store store.Store

	// AskReview makes the pull request's review job due now, under a lease
	// of its own: the one store write an effect makes, and it is to another
	// job. ReviewAsker makes one.
	AskReview func(ctx context.Context, pr store.Subject, now time.Time) error

	// StateDir is where workspaces and their progress live - beside the
	// store, never in it (ADR 0001 §5).
	StateDir string

	// Log receives one line each time the watch finds a failed run on a head
	// the local gate passed: what CI caught that the gate did not (dotfiles
	// ADR 0007 §8). Nil is silent. It is a log rather than a notification: nothing
	// here is the operator's to act on.
	Log func(msg string)
}

// Transitions is the implement kind, as registry entries.
func Transitions(d *Deps) []transition.Transition {
	return []transition.Transition{
		{Name: "implement", Kind: store.KindImplement, From: Start, Run: d.claim},
		{Name: "implement-claimed", Kind: store.KindImplement, From: Claiming, Run: d.claimed},
		{Name: "implement-run", Kind: store.KindImplement, From: Implementing, Tokens: []string{transition.HeavyBuild}, Run: d.run},
		{Name: "implement-gate", Kind: store.KindImplement, From: Gating, Tokens: []string{transition.HeavyBuild}, Run: d.gate},
		{Name: "implement-push", Kind: store.KindImplement, From: Pushing, Run: d.pushTransition},
		{Name: "implement-open", Kind: store.KindImplement, From: Opening, Run: d.openPR},
		{Name: "implement-watch", Kind: store.KindImplement, From: Watching, Run: d.watch},
		{Name: "implement-review", Kind: store.KindImplement, From: Reviewing, Run: d.awaitReview},
		{Name: "implement-hand-off", Kind: store.KindImplement, From: HandingOff, Run: d.handOff},
		{Name: "implement-handed-back", Kind: store.KindImplement, From: HandingBack, Run: d.handedBack},
		{Name: "implement-resume", Kind: store.KindImplement, From: Deferred, Run: d.resume},
	}
}

// claim is `implement`: take every unanswered command, and either start the
// work or say where it already is.
func (d *Deps) claim(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	is, err := d.Tracker.Issue(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	if is.PullRequest {
		// Only a hand-run can get here - intake reads `/implement` on
		// issues alone - and it named the wrong number.
		return transition.Result{}, fmt.Errorf("#%d is a pull request, not an issue", n)
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

	if is.State != "open" {
		// Nothing to implement, and nothing to say: the claims are
		// enough to stop the commands being armed again.
		return book.Owe(ctx, in, Claiming, owed.Record{Next: Start, Items: items})
	}
	pr, ok, err := d.open(ctx, d.forIssue(n))
	if err != nil {
		return transition.Result{}, err
	}
	if ok {
		for _, c := range commands {
			items = append(items, already(n, c, pr))
		}
		return book.Owe(ctx, in, Claiming, owed.Record{Next: Start, Items: items})
	}
	// Whatever an earlier run left is from work that was handed back or
	// never finished, and this is a fresh start.
	if err := d.clear(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	return book.Owe(ctx, in, Claiming, owed.Record{Next: Implementing, Due: true, Items: items})
}

// claimed is `implement-claimed`: on to what the claim decided, once its
// claims and replies are on the tracker. A record lost with the state
// directory sends the job back to claim, which reads the commands afresh.
func (d *Deps) claimed(ctx context.Context, in transition.In) (transition.Result, error) {
	return d.book().Settle(ctx, in, transition.Result{State: Start, RunAt: in.Now})
}

// handedBack is `implement-handed-back`: at rest, once the hand-back's comment
// and its label are both on the tracker. A record lost with the state
// directory rests all the same. Claiming again from there would start the
// work over with nobody asking for it.
func (d *Deps) handedBack(ctx context.Context, in transition.In) (transition.Result, error) {
	return d.book().Settle(ctx, in, transition.Result{State: Start})
}

// book is the implement kind's way to what it owes the tracker.
func (d *Deps) book() *owed.Book {
	return &owed.Book{Tracker: d.Tracker, Store: d.Store, Login: d.Login, Bound: d.Bound, Dir: filepath.Join(d.StateDir, "owed")}
}

// open finds the agent's open pull request that match accepts, if it has one.
// It is recognised by its author and its branch, which are read from the
// tracker rather than remembered in the store (ADR 0001 §5).
func (d *Deps) open(ctx context.Context, match func(github.PullRequest) bool) (github.PullRequest, bool, error) {
	prs, err := d.Tracker.OpenPullRequests(ctx)
	if err != nil {
		return github.PullRequest{}, false, err
	}
	for _, pr := range prs {
		if strings.EqualFold(pr.Login, d.Login) && match(pr) {
			return pr, true, nil
		}
	}
	return github.PullRequest{}, false, nil
}

// forIssue matches a pull request from any branch the agent pushed for issue n.
func (d *Deps) forIssue(n int) func(github.PullRequest) bool {
	return func(pr github.PullRequest) bool {
		issue, ok := IssueOf(d.BranchPrefix, pr.HeadRef)
		return ok && issue == n
	}
}

// from matches a pull request from branch.
func from(branch string) func(github.PullRequest) bool {
	return func(pr github.PullRequest) bool { return pr.HeadRef == branch }
}

// IssueOf is the issue a branch the agent pushed is for: `<prefix><n>-<k>`,
// where k counts the branches pushed for n. A branch not spelled that way is
// not one of the agent's.
func IssueOf(prefix, branch string) (int, bool) {
	rest, ok := strings.CutPrefix(branch, prefix)
	if !ok || prefix == "" {
		return 0, false
	}
	issue, k, ok := strings.Cut(rest, "-")
	if !ok || !digits(issue) || !digits(k) {
		return 0, false
	}
	n, err := strconv.Atoi(issue)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// digits reports whether s is a non-empty run of ASCII digits, which is how the
// agent spells a number in a branch name: no sign, no space.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// already is the reply to a command on an issue whose pull request is open.
func already(n int, c github.Comment, pr github.PullRequest) owed.Item {
	return owed.Reply(fmt.Sprintf("open-pr-comment-%d", c.ID), n, c,
		fmt.Sprintf("Already implemented in #%d, which is still open. Comment on that pull request, or close it and `%s` again to start over.", pr.Number, Word))
}
