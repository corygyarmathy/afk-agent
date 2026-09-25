// Package implement is the implement job kind's transitions: an issue becomes a
// pull request, for a human to review and merge (#40).
//
// The claim (#49), the work (#50), the push (#51) and the CI watch (#52) are
// here. The review follows as its own transitions (#53):
//
//	start        --implement-------->  implementing  claim every unanswered command
//	implementing --implement-run---->  gating        one candidate model, in the workspace
//	gating       --implement-gate--->  pushing       the local gate passed
//	                                   implementing  it failed: back to the session that wrote it
//	                                   start         hand-back on the issue, at rest
//	pushing      --implement-push--->  opening       the denylist, then the push
//	                                   start         a denied path: hand-back on the issue
//	opening      --implement-open--->  watching      the push is on the remote, and so is the pull request
//	                                   opening       the pull request, under the next key
//	                                   pushing       the push is not on the remote: again
//	watching     --implement-watch-->  reviewing     CI is green on the pushed head
//	                                   watching      not finished: again after the CI wait
//	                                   implementing  red: back to the session, with what CI said
//	                                   start         out of rounds, or past the ceiling: hand-back on the pull request
//	deferred     --implement-resume->  implementing  the tier again, from its first model
//
// The claim is its own transition for the reason review's is: it is committed
// before anything can fail. A job that failed ahead of its claim would come to
// rest with its command unanswered, and intake arms a command only once.
//
// The gate is its own transition, and the agent's rather than the model's. The
// session is told to run it, and its word that it did is not the gate. Two
// counts bound the work, and they are kept apart: the job's attempt count
// moves a failing model run to the next candidate, and the progress file
// beside the workspace counts the gate's failures. A candidate that fails
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
	"strconv"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The implement kind's states.
const (
	Start = "start"

	Implementing = "implementing"
	Gating       = "gating"
	Deferred     = "deferred"

	Pushing = "pushing"
	Opening = "opening"

	Watching = "watching"

	// Reviewing is where the review is asked for and waited on. No
	// transition runs from it until #53, so a job that reaches it parks.
	Reviewing = "reviewing"
)

// Word is the command that asks for an issue to be implemented.
const Word = "/implement"

// Tracker is what the implement kind reads and writes. *github.Client is one.
type Tracker interface {
	Issue(ctx context.Context, number int) (github.Issue, error)
	OpenPullRequests(ctx context.Context) ([]github.PullRequest, error)
	Comments(ctx context.Context, number int) ([]github.Comment, error)
	Reactions(ctx context.Context, commentID int64) ([]github.Reaction, error)
	Comment(ctx context.Context, number int, body string) (github.Comment, error)
	React(ctx context.Context, commentID int64, content string) error
	Label(ctx context.Context, number int, label string) error
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

	// HandBackLabel is the label a hand-back applies. A parameter.
	HandBackLabel string

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

	// Store is read, never written: the push and the pull request ask it
	// which round is next. Writing is the runner's.
	Store store.Store

	// StateDir is where workspaces and their progress live - beside the
	// store, never in it (ADR 0001 §5).
	StateDir string
}

// Transitions is the implement kind, as registry entries.
func Transitions(d *Deps) []transition.Transition {
	return []transition.Transition{
		{Name: "implement", Kind: store.KindImplement, From: Start, Run: d.claim},
		{Name: "implement-run", Kind: store.KindImplement, From: Implementing, Tokens: []string{transition.HeavyBuild}, Run: d.run},
		{Name: "implement-gate", Kind: store.KindImplement, From: Gating, Tokens: []string{transition.HeavyBuild}, Run: d.gate},
		{Name: "implement-push", Kind: store.KindImplement, From: Pushing, Run: d.pushTransition},
		{Name: "implement-open", Kind: store.KindImplement, From: Opening, Run: d.openPR},
		{Name: "implement-watch", Kind: store.KindImplement, From: Watching, Run: d.watch},
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
	commands, err := d.unanswered(ctx, comments)
	if err != nil {
		return transition.Result{}, err
	}

	var effects []transition.Effect
	for _, c := range commands {
		effects = append(effects, d.react(c))
	}

	if is.State != "open" {
		// Nothing to implement, and nothing to say: the claims are
		// enough to stop the commands being armed again.
		return transition.Result{State: Start, Effects: effects}, nil
	}
	pr, ok, err := d.open(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	if ok {
		for _, c := range commands {
			effects = append(effects, d.already(n, c, pr))
		}
		return transition.Result{State: Start, Effects: effects}, nil
	}
	// Whatever an earlier run left is from work that was handed back or
	// never finished, and this is a fresh start.
	if err := d.clear(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Implementing, RunAt: in.Now, Effects: effects}, nil
}

// unanswered is the implement commands among comments that nobody has claimed.
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

// open finds the agent's open pull request for issue n, if it has one. It is
// recognised by its author and its branch, which is read from the tracker
// rather than remembered in the store (ADR 0001 §5).
func (d *Deps) open(ctx context.Context, n int) (github.PullRequest, bool, error) {
	prs, err := d.Tracker.OpenPullRequests(ctx)
	if err != nil {
		return github.PullRequest{}, false, err
	}
	for _, pr := range prs {
		if !strings.EqualFold(pr.Login, d.Login) {
			continue
		}
		if issue, ok := IssueOf(d.BranchPrefix, pr.HeadRef); ok && issue == n {
			return pr, true, nil
		}
	}
	return github.PullRequest{}, false, nil
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

func (d *Deps) react(c github.Comment) transition.Effect {
	return transition.Effect{
		Key: fmt.Sprintf("claim-comment-%d", c.ID),
		Do:  func(ctx context.Context) error { return d.Tracker.React(ctx, c.ID, intake.Claim) },
	}
}

func (d *Deps) already(n int, c github.Comment, pr github.PullRequest) transition.Effect {
	return transition.Effect{
		Key: fmt.Sprintf("open-pr-comment-%d", c.ID),
		Do: func(ctx context.Context) error {
			_, err := d.Tracker.Comment(ctx, n, fmt.Sprintf("Already implemented in #%d, which is still open. Comment on that pull request, or close it and `%s` again to start over.", pr.Number, Word))
			return err
		},
	}
}
