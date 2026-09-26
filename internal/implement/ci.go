package implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// passing is the conclusions a finished check run may have and still be
// green: it passed, it said nothing either way, or it did not apply.
var passing = map[string]bool{"success": true, "neutral": true, "skipped": true}

// approval is the conclusion of a run that waits for a human to let it run: a
// failure no session can fix.
const approval = "action_required"

// watch is `implement-watch`: CI's reading of the pushed head, which decides
// correctness where the local gate only decided whether to push (dotfiles
// ADR 0007 §3).
//
// Waiting is a scheduled re-entry, never a process (ADR 0001 §3): a head whose
// checks are not finished puts the job back to sleep for the CI wait. A head
// whose checks never finish looks exactly like a slow one, and only the
// ceiling tells them apart.
func (d *Deps) watch(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return d.lost(ctx, in)
	}
	if err != nil {
		return transition.Result{}, err
	}
	pr, ok, err := d.pullRequestFrom(ctx, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	if !ok {
		// Closed, or merged, by a human. That is their decision about the
		// work, and there is nothing to say about it.
		return transition.Result{State: Start}, d.clear(in.Job.ID)
	}
	// Someone else's push is theirs to see through. It cancels the run on the
	// agent's head, which would read as red, and the lease would refuse every
	// push a fix round made on top of it.
	at, err := remoteHead(ctx, d.Remote, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	if at != p.Pushed {
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, fmt.Sprintf("Someone else pushed to `%s` while CI ran: it is at `%s`, not at `%s` where the agent left it, and the agent does not push over anyone else's work.", p.Branch, short(at), short(p.Pushed)), "")
	}

	runs, err := d.Tracker.CheckRuns(ctx, p.Pushed)
	if err != nil {
		return transition.Result{}, err
	}
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
	output := ciOutput(failed)

	if !finished {
		if !in.Now.Before(p.PushedAt.Add(d.CICeiling)) {
			reason := fmt.Sprintf("CI had not finished on `%s` %s after the push.", short(p.Pushed), d.CICeiling)
			if len(failed) > 0 {
				reason += fmt.Sprintf(" By then %s had failed.", names(failed))
			}
			return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, reason, output)
		}
		return transition.Result{State: Watching, RunAt: in.Now.Add(d.CIWait)}, nil
	}
	if len(failed) == 0 {
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}
	if len(waiting) > 0 {
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, fmt.Sprintf("CI is waiting for approval to run %s, which a fix round cannot give.", names(waiting)), output)
	}

	p.Rounds++
	if p.Rounds > d.CIRounds {
		return d.handBackPR(ctx, in, p, pr.Number, p.Nonce, fmt.Sprintf("CI still failed after %d rounds of fixes.", d.CIRounds), output)
	}
	// Back to the session that wrote the commit (dotfiles ADR 0007 §4),
	// with a fresh count of gate attempts: a round is a new convergence on
	// the local gate, and CI rounds are bounded separately from it (§5).
	p.Attempts = 0
	p.Failure = output
	p.Why = fmt.Sprintf("CI failed on `%s`, the head the agent pushed: %s.", short(p.Pushed), names(failed))
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Implementing, RunAt: in.Now}, nil
}

// lost is work past its push whose workspace or progress went with the state
// directory: in a watch, or in a fix round. A red run could not go back to the
// session that wrote the branch, and a fix round would start a new branch
// beside the pull request. The pull request is handed back instead - once per
// head - or, if there is none, the job rests.
func (d *Deps) lost(ctx context.Context, in transition.In) (transition.Result, error) {
	pr, ok, err := d.open(ctx, in.Job.Subject.Number)
	if err != nil || !ok {
		return transition.Result{State: Start}, errors.Join(err, d.clear(in.Job.ID))
	}
	return d.handBackPR(ctx, in, progress{Branch: pr.HeadRef}, pr.Number, "lost-"+pr.HeadSHA,
		"The agent lost its record of the work - its state directory was wiped - so it cannot watch CI or fix what CI finds.", "")
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
	return tail(b.String(), gateTail)
}

func names(runs []github.CheckRun) string {
	n := make([]string, len(runs))
	for i, r := range runs {
		n[i] = "`" + r.Name + "`"
	}
	return strings.Join(n, ", ")
}

// handBackPR returns the work to a human after the push: a comment saying
// what was tried, and the hand-back label, on the pull request and nowhere
// else, because by then the work and the failure are both the pull request's
// (dotfiles ADR 0007 §2). Never the hand-off. The pull request stays open:
// closing work a human may want is not the agent's to do.
//
// Keyed by what is said once: the progress's nonce, or the head a lost record
// is handed back at. Read back like the hand-back on the issue.
func (d *Deps) handBackPR(ctx context.Context, in transition.In, p progress, pr int, key, reason, output string) (transition.Result, error) {
	marker := handBackMarker(in.Job.Subject.Number, p, key)
	body := handBackBody(marker, "I stopped before handing this pull request off. "+reason, output,
		fmt.Sprintf("The pull request stays open: finish the branch by hand, or close it and `%s` again on #%d.", Word, in.Job.Subject.Number))
	if err := d.clear(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	return d.book().Owe(ctx, in, HandingBack, owed.Record{Next: Start, Items: []owed.Item{
		owed.Comment(fmt.Sprintf("hand-back-pr-%d-%s", pr, key), pr, marker, body),
		owed.Label(fmt.Sprintf("hand-back-label-pr-%d-%s", pr, key), pr, d.HandBackLabel),
	}})
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
