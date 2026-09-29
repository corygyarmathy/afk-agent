package work

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Editor is what edits a pull request's description. *github.Client is one.
type Editor interface {
	EditPullRequest(ctx context.Context, number int, body string) error
}

// OverLimit reports whether GitHub would refuse body as a pull request's or
// an issue's.
func OverLimit(body string) bool {
	return utf8.RuneCountInString(body) > github.BodyLimit
}

// Reread is a pull request's description as it is now: read again as the
// edit is made, so the edit starts from what is there then. ok is false when
// the pull request is no longer there to edit.
type Reread func(ctx context.Context) (body string, ok bool, err error)

// Resensitize is the edit that brings an open pull request's sensitive line to
// touched, the labels the push of head to branch touches, and leaves the rest
// of its description as it was. The line is Go's own fixed part, which the
// written-once rule lets it update: `/implement`'s pushes and `/revise`'s.
//
// It is false when there is nothing to edit, and the caller moves on: the line
// is already right, the description is not one the agent wrote, or the edit
// was made Rounds times for this head and never showed. An edit that never
// lands is logged and costs the work nothing: the description is orientation,
// and the diff is still reviewable without it. The caller reads the pull
// request back after the effect, and calls this again.
//
// The effect reads the description again and edits that, not pr.Body: the
// operator may have edited it since pr was read, and the rest of it is theirs
// to keep. If there is nothing to edit by then, it makes no edit, and the
// read back decides again.
func Resensitize(ctx context.Context, s store.Store, rounds int, ed Editor, reread Reread, pr github.PullRequest, branch, head string, touched []sensitive.Touched, logf func(format string, a ...any)) (transition.Effect, bool, error) {
	_, c := described(pr.Body, touched)
	switch c {
	case noReminder:
		if len(touched) > 0 {
			logf("the description of #%d has no reminder to put the sensitive paths ahead of, so it is left as it is", pr.Number)
		}
		return transition.Effect{}, false, nil
	case alreadyRight:
		return transition.Effect{}, false, nil
	case overLimit:
		logf("the description of #%d would be over GitHub's %d characters even with its sensitive paths counted, so it is left as it is", pr.Number, github.BodyLimit)
		return transition.Effect{}, false, nil
	case counted:
		logf("the description of #%d would be over GitHub's %d characters, so its sensitive paths are counted rather than listed", pr.Number, github.BodyLimit)
	}
	stem := fmt.Sprintf("description-%s-%s", branch, head)
	key, err := transition.Round(ctx, s, stem, 0, rounds)
	if spent, ok := transition.Spent(err); ok {
		logf("the description of #%d was edited %d times for the sensitive paths at `%s` and never showed them, so it is left as it is", pr.Number, spent.Rounds, git.Short(head))
		return transition.Effect{}, false, nil
	}
	if err != nil {
		return transition.Effect{}, false, err
	}
	n := pr.Number
	return transition.Effect{Key: key, Do: func(ctx context.Context) error {
		now, ok, err := reread(ctx)
		if err != nil || !ok {
			return err
		}
		body, c := described(now, touched)
		if c != listed && c != counted {
			return nil
		}
		return ed.EditPullRequest(ctx, n, body)
	}}, true, nil
}

// change is what bringing a description's sensitive line up to date comes to.
type change int

const (
	alreadyRight change = iota // the line is already right
	noReminder                 // not a description the agent wrote
	overLimit                  // over GitHub's limit even with the paths counted
	listed                     // the paths listed
	counted                    // the paths counted, as listing them is over the limit
)

// described is body with its sensitive line brought to touched, and what that
// came to.
func described(body string, touched []sensitive.Touched) (string, change) {
	was := strings.ReplaceAll(body, "\r\n", "\n")
	to, ok := sensitive.With(was, sensitive.Line(touched))
	if !ok {
		return "", noReminder
	}
	// As when it opened, files over GitHub's limit are counted. The
	// session's part is written once, so it is never what gives way here.
	e := listed
	if OverLimit(to) {
		to, _ = sensitive.With(was, sensitive.Counted(touched))
		e = counted
	}
	switch {
	case to == was:
		return "", alreadyRight
	case OverLimit(to):
		return "", overLimit
	}
	return to, e
}
