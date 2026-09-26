// Package review is the review job kind's transitions: advise on a pull
// request, once per head, and never gate it.
//
// A review is six transitions rather than one, because the work is six
// things that fail differently and a transition is the unit that fails
// (ADR 0001 §2):
//
//	start     --review--------->  claiming    claim every unanswered request
//	claiming  --review-claimed->  reviewing   the claims, and any reply, are on the tracker
//	                              start       ... and the head was reviewed already: at rest
//	                              claiming    made again, under the next key
//	reviewing --review-run----->  posting     one candidate model, in a checkout
//	posting   --review-post---->  verifying   post the reply, under a numbered key
//	verifying --review-verify-->  start       at rest, once the reply is on the PR
//	deferred  --review-resume-->  reviewing   the tier again, from its first model
//
// A request is a /review command, or the implement job making this job due
// for the pull request it opened (ADR 0001 §14, as amended for #40). The
// implement job asks by making the job due, never by commenting: a comment the
// agent wrote must never be able to instruct the agent. Its request is claimed
// with a 👀 on the pull request's description, which it wrote, so the claim is
// on what asked and never on anything a human wrote.
//
// Three of the transitions exist for reasons worth stating where the states
// are.
//
// The claim is its own transition so that it is committed before anything can
// fail. A job that failed ahead of its claim would come to rest with its
// command unanswered, and intake arms a command only once.
//
// review-claimed reads the claim back, for the reason review-verify reads the
// review back (package owed): a 👀 lost between the commit and the reaction
// would leave the command looking unanswered for ever.
//
// Verify exists because the runner commits and then performs the effect. A
// process killed between the two loses the comment, and by then the command is
// claimed and armed: nothing would retry it. review-verify reads the answer
// back from the tracker, and sends the job round again under the next key if
// it is not there. The reply is kept in the state directory until then, so a
// re-post does not pay for a second model run.
package review

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
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

// The review kind's states.
const (
	Start     = "start"
	Claiming  = "claiming"
	Reviewing = "reviewing"
	Posting   = "posting"
	Verifying = "verifying"
	Deferred  = "deferred"
)

// Word is the command that asks for a review.
const Word = "/review"

//go:embed prompt.md
var promptText string

var prompt = template.Must(template.New("review").Parse(promptText))

// Tracker is what the review reads and writes. *github.Client is one.
type Tracker interface {
	owed.Tracker
	PullRequest(ctx context.Context, number int) (github.PullRequest, error)
	Diff(ctx context.Context, number int) (string, error)
}

// Model runs one model. opencode.Command is one.
type Model interface {
	Run(ctx context.Context, req opencode.Request) (opencode.Reply, error)
}

// Checkout puts a pull request's head into an empty directory and returns the
// commit it checked out. Git is one.
type Checkout func(ctx context.Context, dir string, number int) (string, error)

// Deps is everything the review's transitions reach. Built once, by the command
// surface; the transitions themselves hold nothing.
type Deps struct {
	Tracker Tracker
	Model   Model

	// Store is read, never written: review-post and review-claimed ask it
	// which round is next. Writing is the runner's.
	Store store.Store

	Checkout Checkout

	// Resolve is the ordered candidate list for a review, as of now: the
	// requirements, the catalogue, the enrolment and the observed budget
	// (ADR 0001 §9). A *model.LimitedError defers the job to the reset.
	Resolve func(ctx context.Context) (model.Candidates, error)

	// Bound is the attempt bound: how many candidates a review tries before
	// the tier counts as exhausted, and how many times a reply or a claim is
	// made before one that never appears is an error. A parameter.
	Bound int

	// TierWait is how long an exhausted tier defers the job. A parameter: the
	// provider gives no timestamp for this, so this is not a defer to a time
	// something else gave, and it is honest about that.
	TierWait time.Duration

	// Login is the agent's own account: whose reaction is a claim, and whose
	// comment carries a review.
	Login string

	// StateDir is where workspaces and replies waiting to be posted live -
	// beside the store, never in it (ADR 0001 §5).
	StateDir string
}

// Transitions is the review kind, as registry entries.
func Transitions(d *Deps) []transition.Transition {
	return []transition.Transition{
		{Name: "review", Kind: store.KindReview, From: Start, Run: d.claim},
		{Name: "review-claimed", Kind: store.KindReview, From: Claiming, Run: d.claimed},
		{Name: "review-run", Kind: store.KindReview, From: Reviewing, Run: d.run},
		{Name: "review-post", Kind: store.KindReview, From: Posting, Run: d.post},
		{Name: "review-verify", Kind: store.KindReview, From: Verifying, Run: d.verify},
		{Name: "review-resume", Kind: store.KindReview, From: Deferred, Run: d.resume},
	}
}

// Marker is the hidden line a review of head carries. Whether a head has been
// reviewed is read from the tracker by it, never from the store, so a wiped
// store forgets its dedup history and not what was reviewed (ADR 0001 §6).
func Marker(head string) string {
	return "<!-- afk:review head=" + head + " -->"
}

// claim is `review`: take every unanswered request, and either start the
// review or say the head has one already.
func (d *Deps) claim(ctx context.Context, in transition.In) (transition.Result, error) {
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
	asked, err := d.askedByJob(ctx, pr)
	if err != nil {
		return transition.Result{}, err
	}
	if asked {
		items = append(items, owed.ClaimPullRequest(n))
	}

	if pr.State != "open" {
		// Nothing to review on a closed pull request, and nothing to say:
		// the claims are enough to stop the commands being armed again.
		return book.Owe(ctx, in, Claiming, owed.Record{Next: Start, Items: items})
	}
	if d.reviewed(comments, pr.HeadSHA) {
		for _, c := range commands {
			items = append(items, already(n, c, pr.HeadSHA))
		}
		return book.Owe(ctx, in, Claiming, owed.Record{Next: Start, Items: items})
	}
	if asked {
		// Recorded here, where the request is taken, for the review to say
		// which job asked. Who wrote the pull request cannot say it: a
		// /review on it later is a human's.
		if err := d.saveAsked(in.Job.ID, d.implementedFor(pr)); err != nil {
			return transition.Result{}, err
		}
	}
	return book.Owe(ctx, in, Claiming, owed.Record{Next: Reviewing, Due: true, Items: items})
}

// claimed is `review-claimed`: on to what the claim decided, once its claims
// and replies are on the tracker. A record lost with the state directory sends
// the job back to claim, which reads the requests afresh.
func (d *Deps) claimed(ctx context.Context, in transition.In) (transition.Result, error) {
	return d.book().Settle(ctx, in, transition.Result{State: Start, RunAt: in.Now})
}

// book is the review's way to what it owes the tracker.
func (d *Deps) book() *owed.Book {
	return &owed.Book{Tracker: d.Tracker, Store: d.Store, Login: d.Login, Bound: d.Bound, Dir: filepath.Join(d.StateDir, "owed")}
}

// run is `review-run`: one candidate model, in a fresh checkout of the head.
func (d *Deps) run(ctx context.Context, in transition.In) (transition.Result, error) {
	ref, until, err := model.Choose(ctx, d.Resolve, in.Job.Stays, d.Bound, in.Now, d.TierWait)
	if err != nil {
		return transition.Result{}, err
	}
	if !until.IsZero() {
		return transition.Result{State: Deferred, RunAt: until}, nil
	}

	ws := filepath.Join(d.StateDir, "workspaces", in.Job.ID)
	// A workspace left by a killed run is stale by definition.
	if err := os.RemoveAll(ws); err != nil {
		return transition.Result{}, err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return transition.Result{}, err
	}
	defer os.RemoveAll(ws)

	n := in.Job.Subject.Number
	head, err := d.Checkout(ctx, ws, n)
	if err != nil {
		return transition.Result{}, err
	}
	diff, err := d.Tracker.Diff(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	if err := os.WriteFile(filepath.Join(ws, ".git", "afk-pr.diff"), []byte(diff), 0o644); err != nil {
		return transition.Result{}, err
	}
	pr, err := d.Tracker.PullRequest(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	spec, err := d.spec(ctx, pr)
	if err != nil {
		return transition.Result{}, err
	}
	if err := os.WriteFile(filepath.Join(ws, ".git", "afk-pr-spec.md"), []byte(spec), 0o644); err != nil {
		return transition.Result{}, err
	}

	var text strings.Builder
	if err := prompt.Execute(&text, struct {
		Number int
		Head   string
	}{n, head}); err != nil {
		return transition.Result{}, err
	}

	reply, err := d.Model.Run(ctx, opencode.Request{Model: ref, Dir: ws, Prompt: text.String()})
	var transient *opencode.TransientError
	if errors.As(err, &transient) {
		// Stay, and the stay moves the next run to the next candidate
		// (ADR 0001 §10). An error returned instead would not: it is an
		// attempt, and every other error here is one that is not the
		// model's.
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	if err != nil {
		return transition.Result{}, err
	}

	issue, err := d.askedFor(in.Job.ID)
	if err != nil {
		return transition.Result{}, err
	}
	if err := d.save(in.Job.ID, pending{Head: head, Body: body(head, ref, reply, issue)}); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Posting, RunAt: in.Now}, nil
}

// post is `review-post`: post the saved reply under the next round's key.
func (d *Deps) post(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		// The state directory lost it. The review has to be written again,
		// and it is only a model run: nothing was posted without it.
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	if err != nil {
		return transition.Result{}, err
	}

	n := in.Job.Subject.Number
	key, err := transition.Round(ctx, d.Store, fmt.Sprintf("review-pr-%d-%s", n, p.Head), d.Bound)
	if err != nil {
		return transition.Result{}, fmt.Errorf("the review of %s never appeared on pull request %d: %w", git.Short(p.Head), n, err)
	}
	effect := transition.Effect{Key: key, Do: func(ctx context.Context) error {
		// The key stops this run posting twice. The tracker is what stops a
		// round that follows a slow success from posting again.
		comments, err := d.Tracker.Comments(ctx, n)
		if err != nil {
			return err
		}
		if d.reviewed(comments, p.Head) {
			return nil
		}
		_, err = d.Tracker.Comment(ctx, n, p.Body)
		return err
	}}
	return transition.Result{State: Verifying, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// verify is `review-verify`: done when the reply is on the pull request, and
// round again when it is not.
func (d *Deps) verify(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	if err != nil {
		return transition.Result{}, err
	}
	comments, err := d.Tracker.Comments(ctx, in.Job.Subject.Number)
	if err != nil {
		return transition.Result{}, err
	}
	if !d.reviewed(comments, p.Head) {
		return transition.Result{State: Posting, RunAt: in.Now}, nil
	}
	for _, path := range []string{d.pendingPath(in.Job.ID), d.askedPath(in.Job.ID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return transition.Result{}, err
		}
	}
	return transition.Result{State: Start}, nil
}

// resume is `review-resume`: the wait is over, and the tier is tried again
// from its first model. Moving state is what clears the stays.
func (d *Deps) resume(_ context.Context, in transition.In) (transition.Result, error) {
	return transition.Result{State: Reviewing, RunAt: in.Now}, nil
}

// implementMarker is the hidden line the implement job's pull request carries
// (implement.PRMarker). Spelled here rather than imported, because implement
// imports this package; a test there holds the two to the same spelling.
var implementMarker = regexp.MustCompile(`<!-- afk:implement issue=(\d+) -->`)

// implementedFor is the issue the pull request is the implement job's work
// for, or zero if it is not the implement job's. The marker counts only on a
// pull request the agent wrote: anyone can type it.
func (d *Deps) implementedFor(pr github.PullRequest) int {
	if !strings.EqualFold(pr.Login, d.Login) {
		return 0
	}
	m := implementMarker.FindStringSubmatch(pr.Body)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// askedByJob reports whether the implement job has asked for a review that
// nobody has claimed: its pull request, written by the agent and carrying its
// marker, with no 👀 from the agent on it yet.
func (d *Deps) askedByJob(ctx context.Context, pr github.PullRequest) (bool, error) {
	if d.implementedFor(pr) == 0 {
		return false, nil
	}
	reactions, err := d.Tracker.IssueReactions(ctx, pr.Number)
	if err != nil {
		return false, err
	}
	return !intake.Claimed(reactions, d.Login), nil
}

// reviewed reports whether the agent has posted a review of head.
func (d *Deps) reviewed(comments []github.Comment, head string) bool {
	return Reviewed(comments, d.Login, head)
}

// Reviewed reports whether login, the agent's account, has posted a review of
// head among comments.
func Reviewed(comments []github.Comment, login, head string) bool {
	marker := Marker(head)
	for _, c := range comments {
		if strings.EqualFold(c.Login, login) && strings.Contains(c.Body, marker) {
			return true
		}
	}
	return false
}

// already is the reply to a command for a head that has its review.
func already(n int, c github.Comment, head string) owed.Item {
	return owed.Reply(fmt.Sprintf("already-comment-%d", c.ID), n, c,
		fmt.Sprintf("Already reviewed at `%s`; nothing has changed since. Push a new commit and `%s` again for another review.", git.Short(head), Word))
}

// body is the comment a review is posted as. On the implement job's pull
// request it says the job asked for it.
func body(head string, ref model.Ref, reply opencode.Reply, issue int) string {
	asked := ""
	if issue != 0 {
		asked = fmt.Sprintf(" Asked for by the implement job for #%d, once CI was green.", issue)
	}
	return fmt.Sprintf("%s\n**Advisory review** of `%s`. This does not gate or block merging.%s\n\n%s\n\n<sub>%s · $%.4f</sub>\n",
		Marker(head), git.Short(head), asked, strings.TrimSpace(reply.Text), ref, reply.Cost)
}

// pending is a reply written and not yet seen on the tracker.
type pending struct {
	Head string `json:"head"`
	Body string `json:"body"`
}

func (d *Deps) pendingPath(jobID string) string {
	return filepath.Join(d.StateDir, "replies", jobID+".json")
}

// save writes the pending reply.
func (d *Deps) save(jobID string, p pending) error {
	return statefile.Save(d.pendingPath(jobID), p)
}

func (d *Deps) load(jobID string) (pending, error) {
	var p pending
	err := statefile.Load(d.pendingPath(jobID), &p)
	if errors.Is(err, os.ErrNotExist) {
		return pending{}, err
	}
	if err != nil {
		return pending{}, fmt.Errorf("pending reply for %s: %w", jobID, err)
	}
	if p.Head == "" || p.Body == "" {
		return pending{}, fmt.Errorf("pending reply for %s is incomplete", jobID)
	}
	return p, nil
}

// asked is the implement job's request, once its claim has been taken: the
// issue its pull request implements. It lasts until the review is on the pull
// request.
type asked struct {
	Issue int `json:"issue"`
}

func (d *Deps) askedPath(jobID string) string {
	return filepath.Join(d.StateDir, "asked", jobID+".json")
}

func (d *Deps) saveAsked(jobID string, issue int) error {
	return statefile.Save(d.askedPath(jobID), asked{Issue: issue})
}

// askedFor is the issue whose implement job asked for this review, or zero if
// nothing but a command did.
func (d *Deps) askedFor(jobID string) (int, error) {
	var a asked
	err := statefile.Load(d.askedPath(jobID), &a)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("the request for %s: %w", jobID, err)
	}
	return a.Issue, nil
}
