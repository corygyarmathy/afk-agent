package review

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/sensitive"
	"github.com/corygyarmathy/afk-agent/internal/spend"
)

// closing is GitHub's closing keywords: the words that link a pull request to
// the issue it closes. A reference into another repository (`owner/name#5`)
// does not match, because the tracker reads this repository only.
var closing = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+#(\d+)\b`)

// partOf is the implement kind's link line on the first piece of an issue too
// big for one pull request: the line straight after its marker, naming the
// issue and the one filed for the rest (#127). Only that line is read, so
// "part of #102" in anyone's prose is not a link.
var partOf = regexp.MustCompile(`\A<!-- afk:implement issue=\d+ -->\r?\nPart of #(\d+)\. The rest is #(\d+)\.\r?(?:\n|\z)`)

// PartOf is the issue a description says its pull request is the first piece
// of, and the issue filed for the rest of it, as the implement kind writes
// them.
func PartOf(desc string) (issue, rest int, ok bool) {
	m := partOf.FindStringSubmatch(desc)
	if m == nil {
		return 0, 0, false
	}
	issue, err1 := strconv.Atoi(m[1])
	rest, err2 := strconv.Atoi(m[2])
	return issue, rest, err1 == nil && err2 == nil
}

// closes is the issues a description closes, in the order it names them, once
// each, and none that is the issue it is part of.
func closes(desc string, partOf int) []int {
	var out []int
	seen := map[int]bool{partOf: true}
	for _, m := range closing.FindAllStringSubmatch(desc, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// spec is what the reviewing-changes skill would have fetched for itself, had
// the workspace any credentials for the tracker: the pull request's
// description, and the issues it closes, verbatim. The description's sensitive
// line is left out: the advisory review is unaware of it (#112), so that it
// reviews a pull request that touches a sensitive path as it does any other.
// Its spend footer goes too: the review is of the work, not of its price.
// Both are taken out of a description the agent wrote and no other, so a
// human's description that quotes either is reviewed as they wrote it.
//
// A first piece is reviewed against the issue it is part of, which is headed
// as only partly done by it, and beside the issue filed for the rest, headed
// as out of scope: without it, everything the piece left for later would read
// as missing (#127).
//
// An issue that is not there - a typo, or one since deleted - is written down
// as a gap rather than failing the review: a review without that issue is
// still worth having, and the reviewer can say what it was missing.
func (d *Deps) spec(ctx context.Context, pr github.PullRequest) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pull request #%d: %s\n\n", pr.Number, pr.Title)
	desc := pr.Body
	if strings.EqualFold(pr.Login, d.Login) {
		desc = spend.Strip(sensitive.Strip(desc))
	}
	if strings.TrimSpace(desc) == "" {
		b.WriteString("(no description)\n")
	} else {
		fmt.Fprintf(&b, "%s\n", desc)
	}

	issue, rest, piece := PartOf(pr.Body)
	write := func(n int, note string) error {
		is, err := d.Tracker.Issue(ctx, n)
		var se *github.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone) {
			fmt.Fprintf(&b, "\n# Issue #%d%s\n\nThis issue could not be read: %s.\n", n, note, se.Status)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "\n# Issue #%d: %s%s\n\n%s\n", is.Number, is.Title, note, is.Body)
		return nil
	}
	if piece {
		if err := write(issue, " (only partly done: this pull request is its first piece)"); err != nil {
			return "", err
		}
		if err := write(rest, fmt.Sprintf(" (out of scope: filed for the rest of #%d)", issue)); err != nil {
			return "", err
		}
	}
	for _, n := range closes(pr.Body, issue) {
		if err := write(n, ""); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}
