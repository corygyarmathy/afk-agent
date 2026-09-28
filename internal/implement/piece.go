package implement

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// remainderFile is where, in the workspace's .git, the session says what is
// left of the issue once its first piece is in: the body of the issue the agent
// files for the rest.
const remainderFile = "afk-remainder.md"

// piece reports whether the work is the first piece of its issue rather than
// the whole of it (#107, #127): cut to one after it came in over the size
// signal, or stopped at one by the session, which said what is left. Its pull
// request is part of the issue, and does not close it.
func (p progress) piece() bool {
	return p.Cut || p.Remainder != ""
}

// linkLine is the description's link line: the issue the pull request closes,
// or the one it is the first piece of, and the issue filed for the rest once
// there is one.
func linkLine(n int, piece bool, rest int) string {
	switch {
	case !piece:
		return fmt.Sprintf("Closes #%d", n)
	case rest == 0:
		return fmt.Sprintf("Part of #%d", n)
	}
	return fmt.Sprintf("Part of #%d. The rest is #%d.", n, rest)
}

// withLink is body with its link line, the one after the marker, made line. It
// reports false for a body with no link line where the agent puts one, which it
// leaves as it is.
func withLink(body, line string) (string, bool) {
	marker, rest, ok := strings.Cut(body, "\n")
	if !ok || !strings.HasPrefix(marker, "<!-- afk:implement ") {
		return body, false
	}
	old, tail, more := strings.Cut(rest, "\n")
	if !strings.HasPrefix(old, "Closes #") && !strings.HasPrefix(old, "Part of #") {
		return body, false
	}
	if !more {
		return marker + "\n" + line, true
	}
	return marker + "\n" + line + "\n" + tail, true
}

// rest is what the pull request of a first piece owes once it is open: the
// issue for what is left, filed once, blocked by the issue, and linked from
// the pull request's link line. Each is read back from the tracker before the
// next is made, and made under the next round's key until it is there. It
// reports true once all of them are, or have run out of rounds and been
// logged.
//
// The pull request opens first because the rest's body links it, and its
// number is not known until it is open. The link line is Go's own part of the
// description, which the written-once rule lets it bring up to date (#111).
//
// The rest carries no label: whether and when it is worked is the operator's
// decision, and the eligibility label would make it the agent's.
func (d *Deps) rest(ctx context.Context, in transition.In, p progress, pr github.PullRequest) (transition.Result, bool, error) {
	n := in.Job.Subject.Number
	again := transition.Result{State: Opening, RunAt: in.Now}
	marker := restMarker(n, p.Branch)

	if p.Rest == 0 {
		found, err := d.filed(ctx, marker)
		if err != nil {
			return transition.Result{}, false, err
		}
		if found == 0 {
			// New with each workspace, as the pull request's stem is.
			stem := fmt.Sprintf("rest-%s-%s", p.Branch, p.Nonce)
			key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
			if spent, ok := transition.Spent(err); ok {
				res, err := d.handBackPR(ctx, in, p, pr.Number, p.Nonce, fmt.Sprintf("This pull request is the first piece of #%d, and the issue for what is left of it was filed %d times and never appeared.", n, spent.Rounds), transition.Noted(d.notePath(in.Job.ID), stem))
				return res, false, err
			}
			if err != nil {
				return transition.Result{}, false, err
			}
			is, err := d.Tracker.Issue(ctx, n)
			if err != nil {
				return transition.Result{}, false, err
			}
			req := github.NewIssue{Title: restTitle(n, is.Title), Body: d.restBody(in.Job.ID, marker, n, pr.Number, p.Remainder)}
			again.Effects = []transition.Effect{{Key: key, Do: transition.Noting(d.notePath(in.Job.ID), stem, func(ctx context.Context) error {
				// The key stops this run filing two. The tracker is what
				// stops a round that follows a slow success from filing
				// another.
				if found, err := d.filed(ctx, marker); err != nil || found != 0 {
					return err
				}
				_, err := d.Tracker.CreateIssue(ctx, req)
				return err
			})}}
			return again, false, nil
		}
		// Kept once seen, so a rest the operator closes straight away is not
		// filed again.
		p.Rest = found
		if err := d.save(in.Job.ID, p); err != nil {
			return transition.Result{}, false, err
		}
	}

	if blocked, err := d.blockedBy(ctx, p.Rest, n); err != nil {
		return transition.Result{}, false, err
	} else if !blocked {
		stem := fmt.Sprintf("rest-blocked-%d-by-%d", p.Rest, n)
		key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
		if spent, ok := transition.Spent(err); ok {
			// The rest is filed and linked either way, and carries no label
			// for the agent, so nothing takes it early for want of this.
			d.logf("%s: #%d was made blocked by #%d %d times and never showed it, so it is left unblocked", in.Job.ID, p.Rest, n, spent.Rounds)
		} else if err != nil {
			return transition.Result{}, false, err
		} else {
			is, err := d.Tracker.Issue(ctx, n)
			if err != nil {
				return transition.Result{}, false, err
			}
			rest, id := p.Rest, is.ID
			again.Effects = []transition.Effect{{Key: key, Do: func(ctx context.Context) error {
				// Adding one that is there already is refused.
				if blocked, err := d.blockedBy(ctx, rest, n); err != nil || blocked {
					return err
				}
				return d.Tracker.AddBlockedBy(ctx, rest, id)
			}}}
			return again, false, nil
		}
	}

	was := strings.ReplaceAll(pr.Body, "\r\n", "\n")
	body, ok := withLink(was, linkLine(n, true, p.Rest))
	switch {
	case !ok:
		d.logf("%s: the description of #%d has no link line to name #%d in, so it is left as it is", in.Job.ID, pr.Number, p.Rest)
	case body == was:
	case over(body):
		d.logf("%s: the description of #%d would be over GitHub's %d characters with #%d in its link line, so it is left as it is", in.Job.ID, pr.Number, bodyLimit, p.Rest)
	default:
		stem := fmt.Sprintf("rest-link-%d-%d", pr.Number, p.Rest)
		key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
		if spent, ok := transition.Spent(err); ok {
			d.logf("%s: the link line of #%d was edited %d times to name #%d and never showed it, so it is left as it is", in.Job.ID, pr.Number, spent.Rounds, p.Rest)
			break
		}
		if err != nil {
			return transition.Result{}, false, err
		}
		number := pr.Number
		again.Effects = []transition.Effect{{Key: key, Do: func(ctx context.Context) error {
			return d.Tracker.EditPullRequest(ctx, number, body)
		}}}
		return again, false, nil
	}
	return transition.Result{}, true, nil
}

// restMarker is the hidden line the issue filed for the rest of issue n's work
// carries, which it is read back by. The branch is new with each try at the
// work, so a later try files a rest of its own.
func restMarker(n int, branch string) string {
	return fmt.Sprintf("<!-- afk:rest issue=%d branch=%s -->", n, branch)
}

// filed is the open issue the agent filed carrying marker, or 0.
func (d *Deps) filed(ctx context.Context, marker string) (int, error) {
	issues, err := d.Tracker.OpenIssues(ctx)
	if err != nil {
		return 0, err
	}
	for _, is := range issues {
		if !is.PullRequest && strings.EqualFold(is.Author, d.Login) && strings.Contains(is.Body, marker) {
			return is.Number, nil
		}
	}
	return 0, nil
}

// blockedBy reports whether issue n is blocked by issue blocker, natively.
func (d *Deps) blockedBy(ctx context.Context, n, blocker int) (bool, error) {
	blockers, err := d.Tracker.BlockedBy(ctx, n)
	if err != nil {
		return false, err
	}
	for _, b := range blockers {
		if b.Number == blocker {
			return true, nil
		}
	}
	return false, nil
}

// restTitle is the rest's title: the issue's, which describes the whole job,
// named as what is left of it. A title too long for GitHub loses its end.
func restTitle(n int, title string) string {
	t := fmt.Sprintf("The rest of #%d: %s", n, title)
	if utf8.RuneCountInString(t) <= titleLimit {
		return t
	}
	r := []rune(t)
	return string(r[:titleLimit-1]) + "…"
}

// restBody is the rest's body: what is left, as the session said it, and the
// pull request that is the first piece. A session's part too long for GitHub
// is left out, as it is from a pull request's description.
func (d *Deps) restBody(jobID, marker string, n, pr int, remainder string) string {
	body := func(remainder string) string {
		if remainder == "" {
			remainder = fmt.Sprintf("The session that made #%d did not say what is left: #%d is the whole job, and #%d the part of it done.", pr, n, pr)
		}
		return fmt.Sprintf("%s\nWhat is left of #%d once #%d, its first piece, is in.\n\n%s\n\nIt is blocked by #%d, which #%d does not close, and it is not labelled for the agent: whether and when it is worked is yours to decide.\n", marker, n, pr, remainder, n, pr)
	}
	b := body(remainder)
	if over(b) {
		d.logf("%s: what the session says is left is over GitHub's %d characters, so the issue for it is filed without it", jobID, bodyLimit)
		b = body("")
	}
	return b
}
