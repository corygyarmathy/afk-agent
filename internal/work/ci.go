package work

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/caught"
	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// passing is the conclusions a finished check run may have and still be
// green: it passed, it said nothing either way, or it did not apply.
var passing = map[string]bool{"success": true, "neutral": true, "skipped": true}

// approval is the conclusion of a run that waits for a human to let it run: a
// failure no session can fix.
const approval = "action_required"

// Checks is what the CI watch reads from the tracker. *github.Client is one.
type Checks interface {
	CheckRuns(ctx context.Context, sha string) ([]github.CheckRun, error)
	RequiredChecks(ctx context.Context, branch string) ([]string, error)
}

// CI is the watch on a pushed head, and its bounds: the same for every kind
// that pushes (#147).
type CI struct {
	Checks Checks

	// Wait is how long a head whose checks are not finished waits before it
	// is looked at again, Ceiling how long after its push they may take
	// before the work is handed back, and Fixes how many times a red run is
	// sent back to the session. Parameters.
	Wait    time.Duration
	Ceiling time.Duration
	Fixes   int

	// Log receives one line for each catch: a run the local gate passed and
	// CI failed. Nil is silent.
	Log func(msg string)
}

// CIState is what the watch found on the pushed head.
type CIState int

const (
	// CIPending is a head whose checks are not finished, inside the ceiling:
	// looked at again at RunAt.
	CIPending CIState = iota
	// CIGreen is every check finished and passing, and every required one
	// there.
	CIGreen
	// CIRed is a head that failed with fixes left. The progress now carries
	// what CI said as the failure, with a fresh count of gate attempts, for
	// the session that wrote the commit.
	CIRed
	// CIHandBack is the work returned to a human on the pull request: out of
	// fixes, past the ceiling, waiting for an approval no fix can give, or
	// the branch moved under the agent. Reason and Output say why.
	CIHandBack
)

// CIResult is what Watch decided. The kind saves the progress and moves the
// job; the words around Reason are its own.
type CIResult struct {
	State CIState

	// RunAt is when a pending head is looked at again.
	RunAt time.Time

	// Reason is a hand-back's sentence, and Output what the failing runs
	// said.
	Reason string
	Output string

	// Moved is a hand-back because someone else pushed to the branch: not
	// anything the agent's own work did, which a correction tells apart
	// from its own failure (package correction).
	Moved bool
}

// Watch is CI's reading of the head the agent pushed to pull request pr, which
// decides correctness where the local gate only decided whether to push
// (dotfiles ADR 0007 §3).
//
// Green is every check run on the head completed with a passing conclusion,
// and a run for every check the pull request's base branch requires. The runs
// alone are not enough: a check that registers late - a job that needs
// others, a second workflow - is absent rather than unfinished, and without
// the required checks a head whose early runs are green would read as green
// before it has run (#66). A missing required check only holds back green:
// once every run there has finished and one has failed, the head is red.
//
// Waiting is a scheduled re-entry, never a process (ADR 0001 §3): a head whose
// checks are not finished is looked at again after the CI wait. A head whose
// checks never finish, or never start, looks exactly like a slow one, and only
// the ceiling tells them apart.
//
// Someone else's push is theirs to see through. It cancels the run on the
// agent's head, which would read as red, and the lease would refuse every push
// a fix made on top of it. So the branch is read first, and a branch not where
// the agent left it is handed back.
func (w Workspace) Watch(ctx context.Context, in transition.In, c CI, pr int, p *Progress) (CIResult, error) {
	at, err := RemoteHead(ctx, w.Remote, p.Branch)
	if err != nil {
		return CIResult{}, err
	}
	if at != p.Pushed {
		return CIResult{State: CIHandBack, Moved: true, Reason: fmt.Sprintf("Someone else pushed to `%s` while CI ran: it is at `%s`, not at `%s` where the agent left it, and the agent does not push over anyone else's work.", p.Branch, git.Short(at), git.Short(p.Pushed))}, nil
	}

	runs, err := c.Checks.CheckRuns(ctx, p.Pushed)
	if err != nil {
		return CIResult{}, err
	}
	required, err := c.Checks.RequiredChecks(ctx, p.Into)
	if err != nil {
		return CIResult{}, err
	}
	missing := absent(required, runs)
	var failed, waiting []github.CheckRun
	finished := len(runs) > 0
	for _, r := range runs {
		switch {
		case r.Status != "completed":
			finished = false
		case r.Conclusion == approval:
			waiting = append(waiting, r)
			failed = append(failed, r)
		case !passing[r.Conclusion]:
			failed = append(failed, r)
		}
	}
	// A missing required check holds back green, never red: a failure is
	// already something to fix, and the fix's head is watched in its turn.
	if len(failed) == 0 && len(missing) > 0 {
		finished = false
	}
	output := ciOutput(failed)

	if !finished {
		if !in.Now.Before(p.PushedAt.Add(c.Ceiling)) {
			reason := fmt.Sprintf("CI had not finished on `%s` %s after the push.", git.Short(p.Pushed), c.Ceiling)
			if len(missing) > 0 {
				reason += fmt.Sprintf(" %s, required on `%s`, had not started.", Quoted(missing), p.Into)
			}
			if len(failed) > 0 {
				reason += fmt.Sprintf(" By then %s had failed.", names(failed))
			}
			c.caught(in, pr, *p, failed)
			return CIResult{State: CIHandBack, Reason: reason, Output: output}, nil
		}
		return CIResult{State: CIPending, RunAt: in.Now.Add(c.Wait)}, nil
	}
	if len(failed) == 0 {
		return CIResult{State: CIGreen}, nil
	}
	c.caught(in, pr, *p, failed)
	if len(waiting) > 0 {
		return CIResult{State: CIHandBack, Reason: fmt.Sprintf("CI is waiting for approval to run %s, which a fix cannot give.", names(waiting)), Output: output}, nil
	}

	// Counted once for each head, so a replay of this decision - its commit
	// lost to a kill, or to the lease - does not count the same red run
	// twice. A fix always pushes a new head: the gate hands back one
	// that adds nothing.
	if p.FixedHead != p.Pushed {
		p.Fixes++
		p.FixedHead = p.Pushed
	}
	if p.Fixes > c.Fixes {
		return CIResult{State: CIHandBack, Reason: fmt.Sprintf("CI still failed after %d fixes.", c.Fixes), Output: output}, nil
	}
	// Back to the session that wrote the commit (dotfiles ADR 0007 §4),
	// with a fresh count of gate attempts: a fix is a new convergence on
	// the local gate, and fixes are bounded separately from it (§5).
	p.Attempts = 0
	p.Failure = output
	p.Why = fmt.Sprintf("CI failed on `%s`, the head the agent pushed: %s.", git.Short(p.Pushed), names(failed))
	return CIResult{State: CIRed}, nil
}

// caught logs what CI caught that the local gate did not: every run on a head
// the gate passed before the push that is a catch as `afk caught` counts one
// (dotfiles ADR 0007 §8). Whether it goes back for a fix or is handed back -
// out of fixes, or at the ceiling with other runs unfinished - the catch is
// the same one. A run that was cancelled, or never ran, is not one.
//
// Logged as it is decided, before the runner commits: a run that fails to
// commit is run again, and logs the catch again.
// `afk caught` counts catches from GitHub rather than from this line
// (ADR 0006), so the second line changes no count.
func (c CI) caught(in transition.In, pr int, p Progress, failed []github.CheckRun) {
	var ran []github.CheckRun
	for _, r := range failed {
		if caught.IsCatch(r.Conclusion) {
			ran = append(ran, r)
		}
	}
	if c.Log == nil || len(ran) == 0 {
		return
	}
	c.Log(fmt.Sprintf("%s: CI caught what the local gate passed, on pull request #%d at %s, fixes so far %d: %s",
		in.Job.ID, pr, git.Short(p.Pushed), p.Fixes, conclusions(ran)))
}

// conclusions is each failing run's name and how it failed.
func conclusions(runs []github.CheckRun) string {
	c := make([]string, len(runs))
	for i, r := range runs {
		c[i] = "`" + r.Name + "` " + r.Conclusion
	}
	return strings.Join(c, ", ")
}

// ciOutput is what the failing check runs said, for the session that has to
// fix them.
func ciOutput(failed []github.CheckRun) string {
	var b strings.Builder
	for _, r := range failed {
		fmt.Fprintf(&b, "## %s: %s\n", r.Name, r.Conclusion)
		if r.URL != "" {
			fmt.Fprintf(&b, "%s\n", r.URL)
		}
		for _, part := range []string{r.Title, r.Summary, r.Text} {
			if part = strings.TrimSpace(part); part != "" {
				fmt.Fprintf(&b, "\n%s\n", part)
			}
		}
		b.WriteString("\n")
	}
	return Tail(b.String(), GateTail)
}

// absent is the required checks with no run on the head.
func absent(required []string, runs []github.CheckRun) []string {
	present := map[string]bool{}
	for _, r := range runs {
		present[r.Name] = true
	}
	var out []string
	for _, name := range required {
		if !present[name] {
			out = append(out, name)
		}
	}
	return out
}

func names(runs []github.CheckRun) string {
	n := make([]string, len(runs))
	for i, r := range runs {
		n[i] = r.Name
	}
	return Quoted(n)
}
