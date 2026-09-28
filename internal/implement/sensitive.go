package implement

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// Sensitive is one kind of sensitive path the operator named: a label, and
// the globs that say which paths it is, matched as the denylist's are
// (denied). A pull request that touches one says so, so that the operator's
// review reads those files line by line (#112). Nothing is ever marked the
// other way.
type Sensitive struct {
	Label string
	Globs []string
}

// ValidSensitive reports the first label that is empty, named twice or has no
// globs, and the first glob that is not well formed. An empty list is the
// feature off.
func ValidSensitive(list []Sensitive) error {
	seen := map[string]bool{}
	for _, s := range list {
		if strings.TrimSpace(s.Label) == "" {
			return fmt.Errorf("a sensitive path has no label")
		}
		if seen[s.Label] {
			return fmt.Errorf("sensitive label %q is given twice", s.Label)
		}
		seen[s.Label] = true
		if len(s.Globs) == 0 {
			return fmt.Errorf("sensitive label %q has no paths", s.Label)
		}
		if err := validGlobs("sensitive", s.Globs); err != nil {
			return err
		}
	}
	return nil
}

// sensitiveLine is the description's line for a pull request that changes
// paths: each label that matched one, in the operator's order, with the files
// it matched. Empty when none did.
func sensitiveLine(list []Sensitive, paths []string) string {
	var labels []string
	for _, s := range list {
		matched := denied(s.Globs, paths)
		if len(matched) == 0 {
			continue
		}
		files := make([]string, len(matched))
		for i, p := range matched {
			files[i] = code(p)
		}
		labels = append(labels, fmt.Sprintf("%s (%s)", s.Label, strings.Join(files, ", ")))
	}
	if len(labels) == 0 {
		return ""
	}
	return sensitivePrefix + strings.Join(labels, ", ")
}

// code is a path as a code span that stays one line and one span, whatever
// the session named its file: a control character is quoted, and the fence
// is longer than any run of backticks in it.
func code(p string) string {
	if strings.ContainsFunc(p, unicode.IsControl) {
		p = strconv.Quote(p)
	}
	fence := "`"
	for strings.Contains(p, fence) {
		fence += "`"
	}
	if len(fence) > 1 {
		return fence + " " + p + " " + fence
	}
	return fence + p + fence
}

// changed is the paths the pull request's diff changes: base to head, net, as
// the pull request shows them. A rename is both of its names.
func changed(ctx context.Context, dir, base, head string) ([]string, error) {
	out, err := git.RunEnv(ctx, dir, git.Isolated, "diff", "--no-renames", "--name-only", "-z", base+"..."+head)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// withSensitive is a description with line as its sensitive line, in Go's
// fixed part ahead of the reminder, and everything from the reminder on as it
// was. It is false for a description with no reminder to find, which the
// agent did not write.
func withSensitive(body, line string) (string, bool) {
	i := strings.Index(body, "\n"+reminder)
	if i < 0 {
		return body, false
	}
	var head []string
	for _, l := range strings.Split(body[:i], "\n") {
		if !strings.HasPrefix(l, sensitivePrefix) {
			head = append(head, l)
		}
	}
	return fixed(strings.TrimRight(strings.Join(head, "\n"), "\n"), line) + body[i+1:], true
}
