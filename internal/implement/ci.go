package implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// passing is the conclusions a finished check run may have and still be
// green: it passed, it said nothing either way, or it did not apply.
var passing = map[string]bool{"success": true, "neutral": true, "skipped": true}

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

	runs, err := d.Tracker.CheckRuns(ctx, p.Pushed)
	if err != nil {
		return transition.Result{}, err
	}
	var failed []github.CheckRun
	finished := len(runs) > 0
	for _, r := range runs {
		switch {
		case r.Status != "completed":
			finished = false
		case !passing[r.Conclusion]:
			failed = append(failed, r)
		}
	}

	if !finished {
		if !in.Now.Before(p.PushedAt.Add(d.CICeiling)) {
			return d.handBackPR(in, p, pr.Number, fmt.Sprintf("CI had not finished on `%s` %s after the push.", short(p.Pushed), d.CICeiling), "")
		}
		return transition.Result{State: Watching, RunAt: in.Now.Add(d.CIWait)}, nil
	}
	if len(failed) == 0 {
		return transition.Result{State: Reviewing, RunAt: in.Now}, nil
	}

	output := ciOutput(failed)
	p.Rounds++
	if p.Rounds > d.CIRounds {
		return d.handBackPR(in, p, pr.Number, fmt.Sprintf("CI still failed after %d rounds of fixes.", d.CIRounds), output)
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

// lost is a watch whose progress went with the state directory. The workspace
// went with it, so a red run could not go back to the session that wrote the
// branch, and a fix round would start a new branch beside the pull request.
// The pull request is handed back instead - once per head - or, if there is
// none, the job rests.
func (d *Deps) lost(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	pr, ok, err := d.open(ctx, n)
	if err != nil || !ok {
		return transition.Result{State: Start}, errors.Join(err, d.clear(in.Job.ID))
	}
	body := handBackBody(n, progress{Branch: pr.HeadRef}, "I stopped before handing this pull request off. The agent lost its record of the work - its state directory was wiped - so it cannot watch CI or fix what CI finds.", "",
		fmt.Sprintf("The pull request stays open: finish the branch by hand, or close it and `%s` again on #%d.", Word, n))
	effects := []transition.Effect{
		{
			Key: fmt.Sprintf("hand-back-pr-%d-lost-%s", pr.Number, pr.HeadSHA),
			Do: func(ctx context.Context) error {
				_, err := d.Tracker.Comment(ctx, pr.Number, body)
				return err
			},
		},
		{
			Key: fmt.Sprintf("hand-back-label-pr-%d-lost-%s", pr.Number, pr.HeadSHA),
			Do:  func(ctx context.Context) error { return d.Tracker.Label(ctx, pr.Number, d.HandBackLabel) },
		},
	}
	return transition.Result{State: Start, Effects: effects}, d.clear(in.Job.ID)
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
func (d *Deps) handBackPR(in transition.In, p progress, pr int, reason, output string) (transition.Result, error) {
	body := handBackBody(in.Job.Subject.Number, p, "I stopped before handing this pull request off. "+reason, output,
		fmt.Sprintf("The pull request stays open: finish the branch by hand, or close it and `%s` again on #%d.", Word, in.Job.Subject.Number))
	effects := []transition.Effect{
		{
			Key: fmt.Sprintf("hand-back-pr-%d-%s", pr, p.Nonce),
			Do: func(ctx context.Context) error {
				_, err := d.Tracker.Comment(ctx, pr, body)
				return err
			},
		},
		{
			Key: fmt.Sprintf("hand-back-label-pr-%d-%s", pr, p.Nonce),
			Do:  func(ctx context.Context) error { return d.Tracker.Label(ctx, pr, d.HandBackLabel) },
		},
	}
	if err := d.clear(in.Job.ID); err != nil {
		return transition.Result{}, err
	}
	return transition.Result{State: Start, Effects: effects}, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
