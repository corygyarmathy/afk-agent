// Package review is the review job kind's transitions: advise on a pull
// request, once per head, and never gate it.
//
// A review is seven transitions rather than one, because the work is seven
// things that fail differently and a transition is the unit that fails
// (ADR 0001 §2):
//
//	start        --review--------------->  claiming      claim every unanswered request
//	claiming     --review-claimed------->  reviewing     the claims, and any reply, are on the tracker
//	                                       start         ... and the head was reviewed already: at rest
//	                                       claiming      made again, under the next key
//	reviewing    --review-run----------->  posting       one candidate model, in a checkout
//	posting      --review-post---------->  verifying     post the reply, under a numbered key
//	                                       handing-back  out of rounds: hand-back on the pull request
//	verifying    --review-verify-------->  start         at rest, once the reply is on the PR
//	handing-back --review-handed-back--->  start         at rest, once the hand-back is on the PR
//	deferred     --review-resume-------->  reviewing     the tier again, from its first model
//
// A request is a /review command, or the implement job making this job due
// for the pull request it opened (ADR 0001 §14, as amended for #40). The
// implement job asks by making the job due, never by commenting: a comment the
// agent wrote must never be able to instruct the agent. Its request is claimed
// with a 👀 on the pull request's description, which it wrote, so the claim is
// on what asked and never on anything a human wrote. The revise job asks the
// same way, once CI is green on a revision, and its request is claimed with a
// 👀 on the reply it posted for that head (#149): not on the description,
// whose 👀 is implement's for the life of the pull request.
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
// re-post does not pay for a second model run. A review that never appears
// after its rounds is handed back rather than posted for ever: a short comment
// saying so may land where the review did not.
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
	"github.com/corygyarmathy/afk-agent/internal/permalink"
	"github.com/corygyarmathy/afk-agent/internal/spend"
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

	HandingBack = "handing-back"
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
	// the tier counts as exhausted. A parameter.
	Bound int

	// Rounds is how many times a review, a claim or a reply is posted before
	// one that never appears counts as never taking effect. A review out of
	// rounds is handed back, with HandBackLabel. Parameters.
	Rounds        int
	HandBackLabel string

	// TierWait is how long an exhausted tier defers the job. A parameter: the
	// provider gives no timestamp for this, so this is not a defer to a time
	// something else gave, and it is honest about that.
	TierWait time.Duration

	// Floor and FoldCut are the reviewing-changes skill's severity floor and
	// fold cut, passed to it in the prompt; the skill does the cutting and
	// the counting. Parameters. Unset, the prompt names neither and the
	// skill's own defaults hold.
	Floor   string
	FoldCut int

	// Repo is the pull request's repository, as owner/name: what the
	// permalinks at the reviewed head that the reply's citations become are
	// built on. Unset, the citations are posted as the model wrote them.
	Repo string

	// Login is the agent's own account: whose reaction is a claim, and whose
	// comment carries a review.
	Login string

	// StateDir is where workspaces and replies waiting to be posted live -
	// beside the store, never in it (ADR 0001 §5).
	StateDir string

	// Log receives one line each time a candidate's run fails transiently:
	// what it failed with, which nothing else keeps once the next candidate
	// runs (#98). Nil is silent. It is a log rather than a notification: a
	// tier that recovers is not the operator's to act on.
	Log func(msg string)
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
		{Name: "review-handed-back", Kind: store.KindReview, From: HandingBack, Run: d.handedBack},
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
	reply, err := d.askedByRevision(ctx, comments, n, pr.HeadSHA)
	if err != nil {
		return transition.Result{}, err
	}
	if reply != 0 {
		items = append(items, owed.Claim(github.Comment{ID: reply}))
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
	if asked || reply != 0 {
		// Recorded here, where the request is taken, for the review to say
		// which job asked. Who wrote the pull request cannot say it: a
		// /review on it later is a human's.
		a := request{Reply: reply}
		if asked {
			a.Issue = d.implementedFor(pr)
		}
		if err := d.saveAsked(in.Job.ID, a); err != nil {
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
	return &owed.Book{Tracker: d.Tracker, Store: d.Store, Login: d.Login, Rounds: d.Rounds, Dir: filepath.Join(d.StateDir, "owed")}
}

// run is `review-run`: one candidate model, in a fresh checkout of the head.
func (d *Deps) run(ctx context.Context, in transition.In) (transition.Result, error) {
	// The stays are the candidates that failed transiently only because
	// that is the one stay this transition makes. Another way to stay here
	// would move the review on to the next candidate as well (#62).
	ref, wait, err := model.Choose(ctx, d.Resolve, in.Job.Stays, d.Bound, in.Now, d.TierWait)
	if err != nil {
		return transition.Result{}, err
	}
	if !wait.Until.IsZero() {
		return transition.Result{State: Deferred, RunAt: wait.Until, Exhausted: wait.Exhausted}, nil
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
	// The revise job asks by making this job due, and one already due -
	// deferred, or on its way here - is left as it is. The reply it left
	// the head with is then a request the claim never saw: back to the
	// claim, which takes it, so the review claims and links it.
	comments, err := d.Tracker.Comments(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	if reply, err := d.askedByRevision(ctx, comments, n, head); err != nil {
		return transition.Result{}, err
	} else if reply != 0 {
		return transition.Result{State: Start, RunAt: in.Now}, nil
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
		Number  int
		Head    string
		Floor   string
		FoldCut int
	}{n, head, d.Floor, d.FoldCut}); err != nil {
		return transition.Result{}, err
	}

	reply, err := d.Model.Run(ctx, opencode.Request{Model: ref, Dir: ws, Prompt: text.String(), Cost: true})
	var transient *opencode.TransientError
	if errors.As(err, &transient) {
		if d.Log != nil {
			d.Log(fmt.Sprintf("%s: %v", in.Job.ID, transient))
		}
		// Paid for, as far as it got: the review that follows says so.
		d.spend(in.Job.ID, ref, reply)
		// A tier with no candidate left defers from here, with the failure
		// that ran it out (#98).
		if wait := model.Failed(ctx, d.Resolve, in.Job.Stays, d.Bound, in.Now, d.TierWait, transient); !wait.Until.IsZero() {
			return transition.Result{State: Deferred, RunAt: wait.Until, Exhausted: wait.Exhausted}, nil
		}
		// Stay, and the stay moves the next run to the next candidate
		// (ADR 0001 §10). An error returned instead would not: it is an
		// attempt, and every other error here is one that is not the
		// model's.
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	if err != nil {
		return transition.Result{}, err
	}
	// Linked while the checkout of head is still here to say which
	// citations name a file.
	if reply.Text, err = permalink.Link(ws, d.Repo, head, reply.Text); err != nil {
		return transition.Result{}, err
	}

	asked, err := d.askedFor(in.Job.ID)
	if err != nil {
		return transition.Result{}, err
	}
	spent := d.spend(in.Job.ID, ref, reply)
	// Posts of a head's review are counted from here, so a review written
	// again for a head whose posts ran out before has an allowance of its
	// own.
	from, err := transition.Next(ctx, d.Store, postStem(n, head))
	if err != nil {
		return transition.Result{}, err
	}
	if err := d.save(in.Job.ID, pending{Head: head, Body: d.body(n, head, reply, asked, spent), From: from, Spent: spent}); err != nil {
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
	stem := postStem(n, p.Head)
	key, err := transition.Round(ctx, d.Store, stem, p.From, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return d.handBack(ctx, in, p, spent.Rounds, transition.Noted(d.notePath(in.Job.ID), stem))
	}
	if err != nil {
		return transition.Result{}, err
	}
	effect := transition.Effect{Key: key, Do: transition.Noting(d.notePath(in.Job.ID), stem, func(ctx context.Context) error {
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
	})}
	return transition.Result{State: Verifying, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// postStem is what the keys of the posts of head's review are made from.
func postStem(n int, head string) string {
	return fmt.Sprintf("review-pr-%d-%s", n, head)
}

// handBack is a review written and never seen on the pull request: a short
// comment saying so, which may land where the review did not, and the
// hand-back label. Read back like any hand-back (package owed), and then at
// rest. Said once for each head.
func (d *Deps) handBack(ctx context.Context, in transition.In, p pending, rounds int, noted string) (transition.Result, error) {
	n := in.Job.Subject.Number
	marker := HandBackMarker(p.Head)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nI wrote a review of `%s`, and posted it %d times, but it never appeared on this pull request.\n", marker, git.Short(p.Head), rounds)
	if noted = strings.TrimSpace(noted); noted != "" {
		fmt.Fprintf(&b, "\nThe last error:\n\n````\n%s\n````\n", noted)
	}
	fmt.Fprintf(&b, "\nPush a new commit and `%s` again for another review.\n", Word)
	if footer := p.Spent.Footer(); footer != "" {
		fmt.Fprintf(&b, "\n%s\n", footer)
	}
	return d.book().Owe(ctx, in, HandingBack, owed.Record{Next: Start, Items: []owed.Item{
		owed.Comment(fmt.Sprintf("review-hand-back-pr-%d-%s", n, p.Head), n, marker, b.String()),
		owed.Label(fmt.Sprintf("review-hand-back-label-pr-%d-%s", n, p.Head), n, d.HandBackLabel),
	}})
}

// handedBack is `review-handed-back`: at rest, once the hand-back's comment
// and its label are on the pull request. A record lost with the state
// directory rests all the same.
//
// The reply is forgotten here rather than when the hand-back is decided. A
// commit lost between the two would otherwise replay the post with no reply,
// and pay for the review again rather than hand it back.
func (d *Deps) handedBack(ctx context.Context, in transition.In) (transition.Result, error) {
	res, err := d.book().Settle(ctx, in, transition.Result{State: Start})
	if err != nil || res.State != Start {
		return res, err
	}
	return res, d.forget(in.Job.ID)
}

// HandBackMarker is the hidden line the hand-back of head's review carries. It
// is not Marker's: a hand-back is not a review.
func HandBackMarker(head string) string {
	return "<!-- afk:review-hand-back head=" + head + " -->"
}

// HandedBack reports whether the agent has handed back its review of head.
func HandedBack(comments []github.Comment, login, head string) bool {
	marker := HandBackMarker(head)
	for _, c := range comments {
		if strings.EqualFold(c.Login, login) && strings.Contains(c.Body, marker) {
			return true
		}
	}
	return false
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
	if err := d.forget(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Start}, nil
}

// forget removes what a review kept while it was on its way to the pull
// request: the reply, the request, what it spent, and the note of its last
// failed post.
func (d *Deps) forget(jobID string) error {
	for _, path := range []string{d.pendingPath(jobID), d.askedPath(jobID), d.spentPath(jobID), d.notePath(jobID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
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

// askedByRevision is the reply the revise job left pull request n at head
// with, if nobody has claimed it: a comment the agent wrote carrying
// owed.RevisionReplyMarker for that head, with no 👀 from the agent on it yet.
// Zero if there is none.
//
// The request is the revise job making this job due, never the comment: a
// comment the agent wrote still instructs nothing. The reply is what that job
// posted, so the claim is on it, as implement's is on the description it
// wrote. A reply for another head is not this review's to claim.
func (d *Deps) askedByRevision(ctx context.Context, comments []github.Comment, n int, head string) (int64, error) {
	marker := owed.RevisionReplyMarker(n, head)
	for _, c := range comments {
		if !strings.EqualFold(c.Login, d.Login) || !strings.Contains(c.Body, marker) {
			continue
		}
		reactions, err := d.Tracker.Reactions(ctx, c.ID)
		if err != nil {
			return 0, err
		}
		if !intake.Claimed(reactions, d.Login) {
			return c.ID, nil
		}
	}
	return 0, nil
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

// body is the comment a review is posted as: all of it inside one <details>,
// under a summary that names the head and nothing else, so that the operator
// reads it after their own reading rather than instead of it (#110). A count
// or a verdict in the summary is what invites a rubber stamp. When a job asked
// for it, it says which: the implement job for its issue, or the revise job,
// linking the reply it claimed.
//
// It is posted once and never edited or deleted: a send-back cites a finding
// by its number in the latest review before it (#123), so a review of a new
// head is a new comment.
func (d *Deps) body(n int, head string, reply opencode.Reply, a request, spent spend.Spent) string {
	asked := ""
	switch {
	case a.Reply != 0 && d.Repo != "":
		asked = fmt.Sprintf(" Asked for by the revise job, once CI was green, with [its reply](https://github.com/%s/pull/%d#issuecomment-%d).", d.Repo, n, a.Reply)
	case a.Reply != 0:
		asked = " Asked for by the revise job, once CI was green, with its reply above."
	case a.Issue != 0:
		asked = fmt.Sprintf(" Asked for by the implement job for #%d, once CI was green.", a.Issue)
	}
	footer := ""
	if f := spent.Footer(); f != "" {
		footer = "\n\n" + f
	}
	return fmt.Sprintf("<details>\n<summary>Advisory review of <code>%s</code>. Open it after your own reading.</summary>\n\n%s\nThis review does not gate or block merging.%s\n\n%s%s\n\n</details>\n",
		git.Short(head), Marker(head), asked, strings.TrimSpace(reply.Text), footer)
}

// spend counts a run of ref, failed or not, into what the review has spent
// since it was claimed, and returns the total (#22). Kept until the review is
// at rest, so the runs that failed before one wrote it are in its footer.
//
// A record that cannot be read or written is logged and costs the review
// nothing but a footer short of some runs: the review is worth having
// without it.
func (d *Deps) spend(jobID string, ref model.Ref, reply opencode.Reply) spend.Spent {
	var s spend.Spent
	if err := statefile.Load(d.spentPath(jobID), &s); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.logf("%s: what the review spent before this run could not be read, so its footer counts from this run: %v", jobID, err)
		s = spend.Spent{}
	}
	s.Add(ref, reply)
	if err := statefile.Save(d.spentPath(jobID), s); err != nil {
		d.logf("%s: what the review spent could not be kept, so a later run's footer will not count it: %v", jobID, err)
	}
	return s
}

func (d *Deps) logf(format string, a ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(format, a...))
	}
}

func (d *Deps) spentPath(jobID string) string {
	return filepath.Join(d.StateDir, "spent", jobID+".json")
}

// pending is a reply written and not yet seen on the tracker. From is the
// round its posts are counted from.
type pending struct {
	Head string `json:"head"`
	Body string `json:"body"`
	From int    `json:"from,omitempty"`

	// Spent is what writing it cost, for the hand-back of a review that
	// never appeared.
	Spent spend.Spent `json:"spent,omitzero"`
}

func (d *Deps) pendingPath(jobID string) string {
	return filepath.Join(d.StateDir, "replies", jobID+".json")
}

// notePath is where a post's last error waits for the decision that reads it
// back (transition.Noting).
func (d *Deps) notePath(jobID string) string {
	return filepath.Join(d.StateDir, "notes", jobID+".json")
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

// request is a job's request, once its claim has been taken: the issue the
// implement job's pull request implements, or the reply the revise job left
// its head with. It lasts until the review is on the pull request.
type request struct {
	Issue int   `json:"issue,omitempty"`
	Reply int64 `json:"reply,omitempty"`
}

func (d *Deps) askedPath(jobID string) string {
	return filepath.Join(d.StateDir, "asked", jobID+".json")
}

func (d *Deps) saveAsked(jobID string, a request) error {
	return statefile.Save(d.askedPath(jobID), a)
}

// askedFor is the request a job made for this review, or the zero request if
// nothing but a command did.
func (d *Deps) askedFor(jobID string) (request, error) {
	var a request
	err := statefile.Load(d.askedPath(jobID), &a)
	if errors.Is(err, os.ErrNotExist) {
		return request{}, nil
	}
	if err != nil {
		return request{}, fmt.Errorf("the request for %s: %w", jobID, err)
	}
	return a, nil
}
