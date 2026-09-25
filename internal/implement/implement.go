// Package implement is the implement job kind's transitions: an issue becomes a
// pull request, for a human to review and merge (#40).
//
// Only the first transition is here yet (#49). The work itself, the push, the
// CI watch and the review each follow as their own transitions (#50-#53):
//
//	start --implement-->  implementing   claim every unanswered command
//
// The claim is its own transition for the reason review's is: it is committed
// before anything can fail. A job that failed ahead of its claim would come to
// rest with its command unanswered, and intake arms a command only once.
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

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The implement kind's states.
const (
	Start = "start"

	// Implementing is where the work starts. No transition runs from it
	// until #50, so a job that reaches it parks.
	Implementing = "implementing"
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
}

// Deps is everything the implement kind's transitions reach. Built once, by
// the command surface; the transitions themselves hold nothing.
type Deps struct {
	Tracker Tracker

	// Login is the agent's own account: whose reaction is a claim, and whose
	// pull requests are the agent's.
	Login string

	// BranchPrefix begins every branch the agent pushes for an issue. A
	// parameter.
	BranchPrefix string
}

// Transitions is the implement kind, as registry entries.
func Transitions(d *Deps) []transition.Transition {
	return []transition.Transition{
		{Name: "implement", Kind: store.KindImplement, From: Start, Run: d.claim},
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
