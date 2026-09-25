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
)

// closing is GitHub's closing keywords: the words that link a pull request to
// the issue it closes. A reference into another repository (`owner/name#5`)
// does not match, because the tracker reads this repository only.
var closing = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+#(\d+)\b`)

// closes is the issues a description closes, in the order it names them, once
// each.
func closes(desc string) []int {
	var out []int
	seen := map[int]bool{}
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
// description, and the issues it closes, verbatim.
//
// An issue that is not there - a typo, or one since deleted - is written down
// as a gap rather than failing the review: a review without that issue is
// still worth having, and the reviewer can say what it was missing.
func (d *Deps) spec(ctx context.Context, pr github.PullRequest) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pull request #%d: %s\n\n", pr.Number, pr.Title)
	if strings.TrimSpace(pr.Body) == "" {
		b.WriteString("(no description)\n")
	} else {
		fmt.Fprintf(&b, "%s\n", pr.Body)
	}

	for _, n := range closes(pr.Body) {
		is, err := d.Tracker.Issue(ctx, n)
		var se *github.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone) {
			fmt.Fprintf(&b, "\n# Issue #%d\n\nThis issue could not be read: %s.\n", n, se.Status)
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n# Issue #%d: %s\n\n%s\n", is.Number, is.Title, is.Body)
	}
	return b.String(), nil
}
