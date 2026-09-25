// Package review is the review job kind's transitions: advise on a pull
// request, once per head, and never gate it.
//
// A review is five transitions rather than one, because the work is five
// things that fail differently and a transition is the unit that fails
// (ADR 0001 §2):
//
//	start     --review-------->  reviewing   claim every unanswered command
//	reviewing --review-run---->  posting     one candidate model, in a checkout
//	posting   --review-post--->  verifying   post the reply, under a numbered key
//	verifying --review-verify->  start       at rest, once the reply is on the PR
//	deferred  --review-resume->  reviewing   the tier again, from its first model
//
// Two of those exist for reasons worth stating where the states are.
//
// The claim is its own transition so that it is committed before anything can
// fail. A job that failed ahead of its claim would come to rest with its
// command unanswered, and intake arms a command only once.
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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The review kind's states.
const (
	Start     = "start"
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
	PullRequest(ctx context.Context, number int) (github.PullRequest, error)
	Issue(ctx context.Context, number int) (github.Issue, error)
	Diff(ctx context.Context, number int) (string, error)
	Comments(ctx context.Context, number int) ([]github.Comment, error)
	Reactions(ctx context.Context, commentID int64) ([]github.Reaction, error)
	Comment(ctx context.Context, number int, body string) (github.Comment, error)
	React(ctx context.Context, commentID int64, content string) error
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

	// Store is read, never written: review-post asks it which posting round is
	// next. Writing is the runner's.
	Store store.Store

	Checkout Checkout

	// Resolve is the ordered candidate list for a review, as of now: the
	// requirements, the catalogue, the enrolment and the observed budget
	// (ADR 0001 §9). A *model.LimitedError defers the job to the reset.
	Resolve func(ctx context.Context) (model.Candidates, error)

	// Bound is the attempt bound: how many candidates a review tries before
	// the tier counts as exhausted, and how many times a reply is posted
	// before a reply that never appears is handed back. A parameter.
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

// claim is `review`: take every unanswered command, and either start the
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
	commands, err := d.unanswered(ctx, comments)
	if err != nil {
		return transition.Result{}, err
	}

	var effects []transition.Effect
	for _, c := range commands {
		effects = append(effects, d.react(c))
	}

	if pr.State != "open" {
		// Nothing to review on a closed pull request, and nothing to say:
		// the claims are enough to stop the commands being armed again.
		return transition.Result{State: Start, Effects: effects}, nil
	}
	if d.reviewed(comments, pr.HeadSHA) {
		for _, c := range commands {
			effects = append(effects, d.already(n, c, pr.HeadSHA))
		}
		return transition.Result{State: Start, Effects: effects}, nil
	}
	return transition.Result{State: Reviewing, RunAt: in.Now, Effects: effects}, nil
}

// run is `review-run`: one candidate model, in a fresh checkout of the head.
func (d *Deps) run(ctx context.Context, in transition.In) (transition.Result, error) {
	candidates, err := d.Resolve(ctx)
	var limited *model.LimitedError
	if errors.As(err, &limited) {
		at := limited.ResetsAt
		if at.IsZero() {
			at = in.Now.Add(d.TierWait)
		}
		return transition.Result{State: Deferred, RunAt: at}, nil
	}
	if err != nil {
		// A capability no enrolled model has, or a tier nobody enrolled: a
		// configuration mistake, and a human's (ADR 0001 §10).
		return transition.Result{}, err
	}

	ref, err := candidates.Attempt(in.Job.Attempts, d.Bound)
	var exhausted *model.ExhaustedError
	if errors.As(err, &exhausted) {
		return transition.Result{State: Deferred, RunAt: in.Now.Add(d.TierWait)}, nil
	}
	if err != nil {
		return transition.Result{}, err
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
		// Stay, and the attempt count moves the next run to the next
		// candidate (ADR 0001 §10).
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	if err != nil {
		return transition.Result{}, err
	}

	if err := d.save(in.Job.ID, pending{Head: head, Body: body(head, ref, reply)}); err != nil {
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
	key, err := d.round(ctx, n, p.Head)
	if err != nil {
		return transition.Result{}, err
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
	if err := os.Remove(d.pendingPath(in.Job.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return transition.Result{}, err
	}
	return transition.Result{State: Start}, nil
}

// resume is `review-resume`: the wait is over, and the tier is tried again
// from its first model. Moving state is what clears the attempt count.
func (d *Deps) resume(_ context.Context, in transition.In) (transition.Result, error) {
	return transition.Result{State: Reviewing, RunAt: in.Now}, nil
}

// unanswered is the review commands among comments that nobody has claimed.
func (d *Deps) unanswered(ctx context.Context, comments []github.Comment) ([]github.Comment, error) {
	var out []github.Comment
	for _, c := range comments {
		if !intake.IsCommand(c, d.Login, Word) {
			continue
		}
		reactions, err := d.Tracker.Reactions(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		if !intake.Claimed(reactions, d.Login) {
			out = append(out, c)
		}
	}
	return out, nil
}

// reviewed reports whether the agent has posted a review of head.
func (d *Deps) reviewed(comments []github.Comment, head string) bool {
	marker := Marker(head)
	for _, c := range comments {
		if strings.EqualFold(c.Login, d.Login) && strings.Contains(c.Body, marker) {
			return true
		}
	}
	return false
}

func (d *Deps) react(c github.Comment) transition.Effect {
	return transition.Effect{
		Key: fmt.Sprintf("claim-comment-%d", c.ID),
		Do:  func(ctx context.Context) error { return d.Tracker.React(ctx, c.ID, intake.Claim) },
	}
}

func (d *Deps) already(n int, c github.Comment, head string) transition.Effect {
	return transition.Effect{
		Key: fmt.Sprintf("already-comment-%d", c.ID),
		Do: func(ctx context.Context) error {
			_, err := d.Tracker.Comment(ctx, n, fmt.Sprintf("Already reviewed at `%s`; nothing has changed since. Push a new commit and `%s` again for another review.", short(head), Word))
			return err
		},
	}
}

// round is the key for the next posting of head's review: the first of
// review-pr-<n>-<head>-<i> not yet reserved. Deterministic across replays of
// the same round, and new for a round that follows one whose post was lost.
func (d *Deps) round(ctx context.Context, n int, head string) (string, error) {
	bound := d.Bound
	if bound < 1 {
		bound = 1
	}
	for i := range bound {
		key := fmt.Sprintf("review-pr-%d-%s-%d", n, head, i)
		reserved, err := d.Store.Reserved(ctx, key)
		if err != nil {
			return "", err
		}
		if !reserved {
			return key, nil
		}
	}
	return "", fmt.Errorf("the review of %s was posted %d times and never appeared on pull request %d", short(head), bound, n)
}

// body is the comment a review is posted as.
func body(head string, ref model.Ref, reply opencode.Reply) string {
	return fmt.Sprintf("%s\n**Advisory review** of `%s`. This does not gate or block merging.\n\n%s\n\n<sub>%s · $%.4f</sub>\n",
		Marker(head), short(head), strings.TrimSpace(reply.Text), ref, reply.Cost)
}

// pending is a reply written and not yet seen on the tracker.
type pending struct {
	Head string `json:"head"`
	Body string `json:"body"`
}

func (d *Deps) pendingPath(jobID string) string {
	return filepath.Join(d.StateDir, "replies", jobID+".json")
}

// save writes the pending reply atomically, so a crash leaves the old file or
// the new one and never half of one.
func (d *Deps) save(jobID string, p pending) error {
	path := d.pendingPath(jobID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (d *Deps) load(jobID string) (pending, error) {
	b, err := os.ReadFile(d.pendingPath(jobID))
	if err != nil {
		return pending{}, err
	}
	var p pending
	if err := json.Unmarshal(b, &p); err != nil {
		return pending{}, fmt.Errorf("pending reply for %s: %w", jobID, err)
	}
	if p.Head == "" || p.Body == "" {
		return pending{}, fmt.Errorf("pending reply for %s is incomplete", jobID)
	}
	return p, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
