// Package permalink links the file:line citations in a model's text to those
// lines at a commit on GitHub.
package permalink

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// citation is what Link reads a text with. Leftmost first, so at any
// point in the reply the first alternative that matches decides: a fenced
// block, a markdown link or a URL is left alone (group 1), an inline code span
// is linked only when all of it is a citation (group 2), and anything else of
// the shape path:line or path:first-last is a citation (groups 3 to 5).
var citation = regexp.MustCompile("(```[\\s\\S]*?```|\\[[^\\]\\n]*\\]\\([^)\\n]*\\)|https?://[^\\s)>]+)|`([^`\\n]*)`|([A-Za-z0-9_./-]+):([0-9]+)(?:-([0-9]+))?")

// whole is a code span's text when all of it is one citation.
var whole = regexp.MustCompile(`^([A-Za-z0-9_./-]+):([0-9]+)(?:-([0-9]+))?$`)

// Link makes each file:line the text cites a link to that line at head, in
// repo (owner/name), so a citation still reaches the line it meant after the
// branch moves on (#110). The links are made here rather than asked of the
// model: a model asked to write them may not, or may write them wrongly, and
// the skills cite a plain file:line.
//
// A citation is linked only when it names a file in dir, the checkout of head,
// so a time of day or a path the model made up stays as it was written. Nothing
// under .git is linked: that is where a workspace keeps the diff and the spec,
// which are not at head. With no repository there is nothing to link to, and the
// text is left as it is.
func Link(dir, repo, head, text string) (string, error) {
	if repo == "" {
		return text, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()

	link := func(path, from, to string) (string, bool) {
		p := filepath.Clean(path)
		if !filepath.IsLocal(p) || strings.SplitN(filepath.ToSlash(p), "/", 2)[0] == ".git" {
			return "", false
		}
		if n, err := strconv.Atoi(from); err != nil || n < 1 {
			return "", false
		}
		if fi, err := root.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return "", false
		}
		u := fmt.Sprintf("https://github.com/%s/blob/%s/%s#L%s", repo, head, filepath.ToSlash(p), from)
		if to != "" {
			u += "-L" + to
		}
		return u, true
	}

	var b strings.Builder
	last := 0
	for _, m := range citation.FindAllStringSubmatchIndex(text, -1) {
		group := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		match := text[m[0]:m[1]]
		var u string
		var ok bool
		switch {
		case m[2] >= 0:
		case m[4] >= 0:
			if w := whole.FindStringSubmatch(group(2)); w != nil {
				u, ok = link(w[1], w[2], w[3])
			}
		default:
			u, ok = link(group(3), group(4), group(5))
		}
		if !ok {
			continue
		}
		b.WriteString(text[last:m[0]])
		fmt.Fprintf(&b, "[%s](%s)", match, u)
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String(), nil
}
