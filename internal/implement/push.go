package implement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/permalink"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/size"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
)

// pushTransition is `implement-push`: the denylist, then the push, with nothing
// between the two (dotfiles ADR 0007 §6).
//
// The check is made on the relay's copy of the branch, at the commit the push
// will send, and the push sends that commit by name. Between the two there is
// only the commit of this decision: nothing the model runs, and nothing that
// could move what is pushed.
func (d *Deps) pushTransition(ctx context.Context, in transition.In) (transition.Result, error) {
	p, err := d.load(in.Job.ID)
	ws := d.work().Dir(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !d.work().Exists(in.Job.ID)) {
		// The work is gone before it reached the remote, or with nothing
		// to show that it did. Either way it starts over, on a branch
		// nobody has pushed.
		return transition.Result{State: Implementing, RunAt: in.Now}, d.clear(in.Job.ID)
	}
	if err != nil {
		return transition.Result{}, err
	}
	if p.Cutting {
		// The cut was decided, and the commit that sent the work back for
		// it never happened. The work is still uncut, and is not pushed.
		return transition.Result{State: Implementing, RunAt: in.Now}, nil
	}

	relayDir := d.work().RelayDir(in.Job.ID)
	head, err := work.Relay(ctx, ws, relayDir, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	paths, err := work.Touched(ctx, relayDir, p.Base, head)
	if err != nil {
		return transition.Result{}, err
	}
	if bad := work.Denied(d.Denylist, paths); len(bad) > 0 {
		return d.handBack(ctx, in, p, fmt.Sprintf("The work touches %s, which the denylist does not let the agent push.", quoted(bad)), "")
	}

	// Measured here, on the commit the push sends and in the relay, where
	// nothing the session wrote into its .git is read. Whether it is over
	// is decided once the push has landed: the work is kept either way.
	c, err := size.Measure(ctx, relayDir, p.Base, head)
	if err != nil {
		return transition.Result{}, err
	}
	p.Lines, p.Tests = c.Lines, c.Tests

	// Over the signal before its first push, the work goes back to its
	// session to be cut to a first coherent piece, once (#107, #127). What
	// comes back is gated and measured again, and pushed either way: still
	// over, it is handed back when it would open. After the first push a
	// fix is not cut: the pull request is the operator's to read by then.
	if c.Over(d.SizeSignal) && !p.Cut && p.Pushed == "" {
		instructions, err := d.instructions(ctx, in.Job.ID, in.Job.Subject.Number)
		if err != nil {
			return transition.Result{}, err
		}
		if !Whole(instructions) {
			if res, ok, err := d.keepWhole(ctx, in, p, relayDir, head); err != nil || ok {
				return res, err
			}
		}
	}

	// Recomputed with each push, on what the pull request will show: a fix
	// that newly touches a sensitive path adds it.
	p.Sensitive = nil
	if len(d.Sensitive) > 0 {
		paths, err := sensitive.Changed(ctx, relayDir, p.Base, head)
		if err != nil {
			return transition.Result{}, err
		}
		p.Sensitive = sensitive.Touches(d.Sensitive, paths)
	}

	// The stem is new with each head, and a head is pushed only by the work
	// that made it: out of rounds, the work is handed back, and a later
	// command starts it over on a new branch.
	stem := fmt.Sprintf("push-%s-%s", p.Branch, head)
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return d.handBack(ctx, in, p, fmt.Sprintf("The push of `%s` to `%s` was made %d times and never landed.", git.Short(head), p.Branch, spent.Rounds), transition.Noted(d.notePath(in.Job.ID), stem))
	}
	if err != nil {
		return transition.Result{}, err
	}
	p.Head = head
	if err := d.save(in.Job.ID, p); err != nil {
		return transition.Result{}, err
	}
	effect := transition.Effect{Key: key, Do: transition.Noting(d.notePath(in.Job.ID), stem, func(ctx context.Context) error {
		return work.Push(ctx, relayDir, d.Remote, head, p.Branch, p.Pushed)
	})}
	return transition.Result{State: Opening, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// keepWhole is work over the signal on its way to be cut: first pushed as it is
// to wholeBranch, and read back there, then sent to its session to be cut. A
// cut that then fails - a red gate, nothing committed, the branch switched -
// hands back pointing at the work it was cut from rather than losing it, and
// the rest's issue names it, where what is left may be written already.
//
// It reports false when the whole cannot be kept: a branch of that name the
// agent did not push, or a push that never landed. The work is then not cut,
// and goes on to be pushed as it is, which hands it back as over the signal,
// kept on its own branch.
//
// The cut is one bounded round, like a fix: the gate's attempts start again
// for it.
func (d *Deps) keepWhole(ctx context.Context, in transition.In, p progress, relayDir, head string) (transition.Result, bool, error) {
	whole := wholeBranch(p.Branch)
	at, err := work.RemoteHead(ctx, d.Remote, whole)
	if err != nil {
		return transition.Result{}, false, err
	}
	switch {
	case at == head:
		p.Uncut = head
		p.Cut, p.Cutting = true, true
		p.Attempts = 0
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, false, err
		}
		return transition.Result{State: Implementing, RunAt: in.Now}, true, nil
	case at != "":
		d.logf("%s: `%s` is at `%s`, which the agent did not push, so the work is not cut: it is pushed as it is", in.Job.ID, whole, git.Short(at))
		return transition.Result{}, false, nil
	}
	stem := fmt.Sprintf("whole-%s-%s", p.Branch, head)
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		d.logf("%s: the push of `%s` to `%s` was made %d times and never landed, so the work is not cut: it is pushed as it is", in.Job.ID, git.Short(head), whole, spent.Rounds)
		return transition.Result{}, false, nil
	}
	if err != nil {
		return transition.Result{}, false, err
	}
	// Read back by the next pass here: pushing is where the job stays until
	// the whole is seen on the remote.
	effect := transition.Effect{Key: key, Do: func(ctx context.Context) error {
		return work.Push(ctx, relayDir, d.Remote, head, whole, "")
	}}
	return transition.Result{State: Pushing, RunAt: in.Now, Effects: []transition.Effect{effect}}, true, nil
}

// openPR is `implement-open`: once the push is on the remote, the pull request.
//
// It reads both back from the tracker rather than trusting the effect that
// made them. The runner commits and then performs, so a process killed between
// the two loses the effect with its key reserved; this is what notices, and
// sends the job round again under the next key. A push the lease will always
// refuse is not sent round: it is handed back.
func (d *Deps) openPR(ctx context.Context, in transition.In) (transition.Result, error) {
	n := in.Job.Subject.Number
	p, err := d.load(in.Job.ID)
	if errors.Is(err, os.ErrNotExist) {
		// The state directory lost it. The tracker still says whether the
		// pull request was opened, and if it was, the work goes on from
		// there. If it was not, the work starts over on the next free
		// branch. One the push may have made is left where it is: with
		// the progress went the lease, and without it the agent cannot
		// tell its own push from anyone else's.
		if _, ok, err := d.open(ctx, d.forIssue(n)); err != nil {
			return transition.Result{}, err
		} else if ok {
			return transition.Result{State: Watching, RunAt: in.Now}, nil
		}
		return transition.Result{State: Implementing, RunAt: in.Now}, d.clear(in.Job.ID)
	}
	if err != nil {
		return transition.Result{}, err
	}
	at, err := work.RemoteHead(ctx, d.Remote, p.Branch)
	if err != nil {
		return transition.Result{}, err
	}
	if at != p.Head {
		if at != p.Pushed {
			// Neither the agent's push nor the lease it was pinned to:
			// someone else pushed to the branch, or deleted it. Every push
			// from here is refused by the lease, so none is made.
			return d.handBack(ctx, in, p, fmt.Sprintf("Someone else changed `%s` before the agent's push of `%s` landed: %s, and the agent does not push over anyone else's work.", p.Branch, git.Short(p.Head), where(at, p.Pushed)),
				transition.Noted(d.notePath(in.Job.ID), fmt.Sprintf("push-%s-%s", p.Branch, p.Head)))
		}
		return transition.Result{State: Pushing, RunAt: in.Now}, nil
	}
	if p.Pushed != at {
		// Seen on the remote: the lease the next push is pinned to, and
		// the head CI is watched on from now.
		p.Pushed, p.PushedAt = at, in.Now
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, err
		}
	}

	if pr, ok, err := d.open(ctx, from(p.Branch)); err != nil {
		return transition.Result{}, err
	} else if ok {
		return d.resensitize(ctx, in, p, pr)
	}

	// Over the size signal, the pushed branch is the work's to keep, and a
	// human decides what becomes of it. Only the command's instructions can
	// ask for it opened whatever its size.
	if (size.Count{Lines: p.Lines, Tests: p.Tests}).Over(d.SizeSignal) {
		instructions, err := d.instructions(ctx, in.Job.ID, n)
		if err != nil {
			return transition.Result{}, err
		}
		if !Whole(instructions) {
			kept := "The branch is the work, kept: to have it as one pull request, open one from it by hand."
			if p.Cut {
				kept = fmt.Sprintf("It went back to the session once to be cut to a first coherent piece, and is still over: the session found no piece, or cut too little. The branch is what the cut left, and `%s` the work before it, both kept: to have either as one pull request, open one from it by hand.", wholeBranch(p.Branch))
			}
			return d.handBackIssue(ctx, in, p, fmt.Sprintf("The work is %d changed lines, and %d changed lines of tests, which is over the size signal of %d: more than one concern, or more than one sitting's review. %s To have it in pieces, split the issue.", p.Lines, p.Tests, d.SizeSignal, kept), "")
		}
	}

	// A first piece's rest is filed and blocked before its pull request
	// opens, which then names it.
	if p.piece() {
		var res transition.Result
		var done bool
		if p, res, done, err = d.rest(ctx, in, p); err != nil || !done {
			return res, err
		}
	}

	// New with each workspace, as the push's stem is with each head. The
	// branch alone is not: its name is free again once it is gone from the
	// remote, and an earlier job's rounds under it are not this job's.
	stem := fmt.Sprintf("pull-request-%s-%s", p.Branch, p.Nonce)
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		return d.handBackIssue(ctx, in, p, fmt.Sprintf("Its pull request was asked for %d times and never opened.", spent.Rounds), transition.Noted(d.notePath(in.Job.ID), stem))
	}
	if err != nil {
		return transition.Result{}, err
	}
	is, err := d.Tracker.Issue(ctx, n)
	if err != nil {
		return transition.Result{}, err
	}
	// A first piece is part of the issue, and takes the title the session
	// gave it: the issue's describes the whole job (#111, #127). A pull
	// request that closes the issue keeps the issue's title, whatever the
	// file says, and so does a piece the session gave no title.
	title, session := sessionPart(p.Description)
	if !p.piece() || title == "" {
		title = is.Title
	}
	link := linkLine(n, p.piece(), p.Rest)
	if p.Unblocked {
		link += "\n\n" + unblockedNote(n, p.Rest)
	}
	if session == "" && strings.TrimSpace(p.Description) != "" {
		d.logf("%s: the description file has no %q section, so the pull request opens with the agent's parts only", in.Job.ID, "## "+sections[0])
	}
	// Linked in the workspace, the checkout of the pushed head, which says
	// which citations name a file. Without it, nothing is linked.
	if ws := d.work().Dir(in.Job.ID); session != "" && d.work().Exists(in.Job.ID) {
		if session, err = permalink.Link(ws, d.Repo, p.Head, session); err != nil {
			return transition.Result{}, err
		}
	}
	// Over GitHub's limit, the pull request is refused every round. The
	// sensitive files go first, as a count for each label: the diff names
	// them again. Cutting the session's part would drop what the operator
	// needed, so if it is still over, that part goes whole, as a missing one
	// does.
	body := description(n, link, d.ReviewProcedure, sensitive.Line(p.Sensitive), session)
	if over(body) && len(p.Sensitive) > 0 {
		d.logf("%s: the description is over GitHub's %d characters, so its sensitive paths are counted rather than listed", in.Job.ID, bodyLimit)
		body = description(n, link, d.ReviewProcedure, sensitive.Counted(p.Sensitive), session)
	}
	if over(body) {
		d.logf("%s: the description is over GitHub's %d characters, so the pull request opens with the agent's parts only", in.Job.ID, bodyLimit)
		body = description(n, link, d.ReviewProcedure, sensitive.Counted(p.Sensitive), "")
	}
	req := github.NewPullRequest{Title: title, Head: p.Branch, Base: p.Into, Body: body}
	effect := transition.Effect{Key: key, Do: transition.Noting(d.notePath(in.Job.ID), stem, func(ctx context.Context) error {
		// The key stops this run opening two. The tracker is what stops a
		// round that follows a slow success from opening another.
		if _, ok, err := d.open(ctx, from(p.Branch)); err != nil || ok {
			return err
		}
		_, err := d.Tracker.CreatePullRequest(ctx, req)
		return err
	})}
	return transition.Result{State: Opening, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// resensitize brings the open pull request's sensitive line to what the push
// just seen on the remote touches, and leaves the rest of its description as
// it was. It is Go's own fixed part, which the written-once rule lets it
// update. An edit that never lands is logged and costs the work nothing: the
// description is orientation, and the diff is still reviewable without it.
func (d *Deps) resensitize(ctx context.Context, in transition.In, p progress, pr github.PullRequest) (transition.Result, error) {
	watch := transition.Result{State: Watching, RunAt: in.Now}
	was := strings.ReplaceAll(pr.Body, "\r\n", "\n")
	body, ok := sensitive.With(was, sensitive.Line(p.Sensitive))
	if !ok {
		if len(p.Sensitive) > 0 {
			d.logf("%s: the description of #%d has no reminder to put the sensitive paths ahead of, so it is left as it is", in.Job.ID, pr.Number)
		}
		return watch, nil
	}
	// As when it opened, files over GitHub's limit are counted. The
	// session's part is written once, so it is never what gives way here.
	counted := over(body)
	if counted {
		body, _ = sensitive.With(was, sensitive.Counted(p.Sensitive))
	}
	if body == was {
		return watch, nil
	}
	if over(body) {
		d.logf("%s: the description of #%d would be over GitHub's %d characters even with its sensitive paths counted, so it is left as it is", in.Job.ID, pr.Number, bodyLimit)
		return watch, nil
	}
	if counted {
		d.logf("%s: the description of #%d would be over GitHub's %d characters, so its sensitive paths are counted rather than listed", in.Job.ID, pr.Number, bodyLimit)
	}
	stem := fmt.Sprintf("description-%s-%s", p.Branch, p.Head)
	key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
	if spent, ok := transition.Spent(err); ok {
		d.logf("%s: the description of #%d was edited %d times for the sensitive paths at `%s` and never showed them, so it is left as it is", in.Job.ID, pr.Number, spent.Rounds, git.Short(p.Head))
		return watch, nil
	}
	if err != nil {
		return transition.Result{}, err
	}
	effect := transition.Effect{Key: key, Do: func(ctx context.Context) error {
		return d.Tracker.EditPullRequest(ctx, pr.Number, body)
	}}
	return transition.Result{State: Opening, RunAt: in.Now, Effects: []transition.Effect{effect}}, nil
}

// PRMarker is the hidden line the description of the agent's pull request for
// an issue carries.
func PRMarker(issue int) string {
	return fmt.Sprintf("<!-- afk:implement issue=%d -->", issue)
}

// where says where a branch the agent was about to push is, when it is not
// where the agent left it.
func where(at, lease string) string {
	switch {
	case at == "":
		return "it has been deleted"
	case lease == "":
		return fmt.Sprintf("it is at `%s`, which the agent did not push", git.Short(at))
	}
	return fmt.Sprintf("it is at `%s`, not at `%s` where the agent left it", git.Short(at), git.Short(lease))
}

func quoted(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = "`" + p + "`"
	}
	return strings.Join(q, ", ")
}
