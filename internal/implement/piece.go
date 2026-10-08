package implement

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/transition"
	"github.com/corygyarmathy/afk-agent/internal/work"
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
// or the one it is the first piece of, and the issue filed for the rest, or the
// one it refers to without closing. The advisory review reads the line back
// (review.PartOf, review.Refs), so they are spelled alike.
//
// Work with an acceptance criterion it cannot meet by itself - one that needs
// a deploy or a hand run - refers to its issue rather than closing it, so that
// merging it does not close an issue with a check still to do (#199). A first
// piece does not close its issue either way.
func linkLine(n int, piece, unmet bool, rest int) string {
	switch {
	case piece:
		return fmt.Sprintf("Part of #%d. The rest is #%d.", n, rest)
	case unmet:
		return fmt.Sprintf("Refs #%d", n)
	}
	return fmt.Sprintf("Closes #%d", n)
}

// unblockedNote is what a first piece's description says under its link line
// when its rest could not be made blocked by the issue: nothing fails for want
// of the dependency, and the operator can add it by hand.
func unblockedNote(n, rest int) string {
	return fmt.Sprintf("#%d could not be made blocked by #%d: add the dependency by hand if it should wait for this issue.", rest, n)
}

// rest is what a first piece owes before its pull request opens: the issue for
// what is left, filed once, and blocked by the issue. Each is read back from
// the tracker before the next is made, and made under the next round's key
// until it is there. It reports true, with the progress as it now is, once
// both are.
//
// Both come before the pull request, so that it opens once, naming the rest in
// its link line, and GitHub's cross-reference puts it on the rest's timeline.
// An open pull request has had its rest, whatever became of the progress since.
//
// A rest that never appears hands the work back on the issue, as a pull
// request that never opens does. A dependency that never appears does not:
// the rest carries no label for the agent, so nothing takes it early for want
// of one, and the pull request's description says it is missing. Seen once,
// the dependency is not made again, so one the operator removes stays removed.
//
// The rest carries no label: whether and when it is worked is the operator's
// decision, and the eligibility label would make it the agent's.
func (d *Deps) rest(ctx context.Context, in transition.In, p progress) (progress, transition.Result, bool, error) {
	n := in.Job.Subject.Number
	again := transition.Result{State: Opening, RunAt: in.Now}
	marker := restMarker(n, p.Branch)

	if p.Rest == 0 {
		found, err := d.filed(ctx, marker)
		if err != nil {
			return p, transition.Result{}, false, err
		}
		if found == 0 {
			// New with each workspace, as the pull request's stem is.
			stem := fmt.Sprintf("rest-%s-%s", p.Branch, p.Nonce)
			key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
			if spent, ok := transition.Spent(err); ok {
				res, err := d.handBackIssue(ctx, in, p, fmt.Sprintf("The work is the first piece of #%d, and the issue for what is left of it was filed %d times and never appeared.", n, spent.Rounds), transition.Noted(d.notePath(in.Job.ID), stem))
				return p, res, false, err
			}
			if err != nil {
				return p, transition.Result{}, false, err
			}
			is, err := d.Tracker.Issue(ctx, n)
			if err != nil {
				return p, transition.Result{}, false, err
			}
			req := github.NewIssue{Title: restTitle(n, is.Title), Body: d.restBody(in.Job.ID, marker, n, p)}
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
			return p, again, false, nil
		}
		p.Rest = found
		if err := d.save(in.Job.ID, p); err != nil {
			return p, transition.Result{}, false, err
		}
	}

	if !p.Blocked && !p.Unblocked {
		blocked, err := d.blockedBy(ctx, p.Rest, n)
		if err != nil {
			return p, transition.Result{}, false, err
		}
		if !blocked {
			stem := fmt.Sprintf("rest-blocked-%d-by-%d", p.Rest, n)
			key, err := transition.Round(ctx, d.Store, stem, 0, d.Rounds)
			if _, ok := transition.Spent(err); !ok {
				if err != nil {
					return p, transition.Result{}, false, err
				}
				is, err := d.Tracker.Issue(ctx, n)
				if err != nil {
					return p, transition.Result{}, false, err
				}
				rest, id := p.Rest, is.ID
				again.Effects = []transition.Effect{{Key: key, Do: func(ctx context.Context) error {
					// Adding one that is there already is refused.
					if blocked, err := d.blockedBy(ctx, rest, n); err != nil || blocked {
						return err
					}
					return d.Tracker.AddBlockedBy(ctx, rest, id)
				}}}
				return p, again, false, nil
			}
		}
		p.Blocked, p.Unblocked = blocked, !blocked
		if err := d.save(in.Job.ID, p); err != nil {
			return p, transition.Result{}, false, err
		}
	}
	return p, transition.Result{}, true, nil
}

// restMarker is the hidden line the issue filed for the rest of issue n's work
// carries, which it is read back by. The branch is new with each try at the
// work, so a later try files a rest of its own.
func restMarker(n int, branch string) string {
	return fmt.Sprintf("<!-- afk:rest issue=%d branch=%s -->", n, branch)
}

// filed is the issue the agent filed carrying marker, open or closed, or 0. A
// rest the operator closes before it is read back is still filed.
func (d *Deps) filed(ctx context.Context, marker string) (int, error) {
	issues, err := d.Tracker.IssuesBy(ctx, d.Login)
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
// branch that is the first piece. The piece's pull request is not open yet: it
// names the rest, and GitHub puts it on the rest's timeline. Work cut to its
// piece names the branch it was kept on as it was, where what is left may
// already be written. A session's part too long for GitHub is left out, as it
// is from a pull request's description.
func (d *Deps) restBody(jobID, marker string, n int, p progress) string {
	body := func(remainder string) string {
		if remainder == "" {
			remainder = fmt.Sprintf("The session that made `%s` did not say what is left: #%d is the whole job, and `%s` the part of it done.", p.Branch, n, p.Branch)
		}
		uncut := ""
		if p.Uncut != "" {
			uncut = fmt.Sprintf(" The work as it was before it was cut to that piece is on `%s`, at `%s`: some of what is left may be written there already.", wholeBranch(p.Branch), git.Short(p.Uncut))
		}
		return fmt.Sprintf("%s\nWhat is left of #%d once `%s`, its first piece, is in.%s\n\n%s\n\nIt is blocked by #%d, which the first piece does not close, and it is not labelled for the agent: whether and when it is worked is yours to decide.\n", marker, n, p.Branch, uncut, remainder, n)
	}
	b := body(p.Remainder)
	if work.OverLimit(b) {
		d.logf("%s: what the session says is left is over GitHub's %d characters, so the issue for it is filed without it", jobID, github.BodyLimit)
		b = body("")
	}
	return b
}
