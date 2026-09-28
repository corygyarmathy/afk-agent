// Package sensitive is the sensitive paths: the paths the operator named as
// deserving closer reading, and the line a pull request's description carries
// when it touches one, so that the operator's review reads those files line by
// line (#112). Nothing is ever marked the other way.
//
// The line is Go's, in its fixed part of the description ahead of the
// reminder, and recomputed on every push to the pull request: `/implement`'s,
// and `/revise`'s. Go matches the pull request's changed paths against the
// operator's globs (package glob); no model rates anything. The advisory
// review is unaware of it, and reads the description without it (Strip).
package sensitive

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/glob"
)

// Path is one kind of sensitive path the operator named: a label, and the
// globs that say which paths it is.
type Path struct {
	Label string
	Globs []string
}

// Valid reports the first label that is empty, named twice, has no globs or
// could not be read back out of the line, and the first glob that is not well
// formed. An empty list is the feature off.
func Valid(list []Path) error {
	seen := map[string]bool{}
	for _, s := range list {
		if strings.TrimSpace(s.Label) == "" {
			return fmt.Errorf("a sensitive path has no label")
		}
		// The line is one line, and names each label as `label (files)`
		// in a list separated by commas.
		if strings.ContainsFunc(s.Label, unicode.IsControl) || strings.ContainsAny(s.Label, ",()") {
			return fmt.Errorf("sensitive label %q has a control character, a comma or a parenthesis in it", s.Label)
		}
		if seen[s.Label] {
			return fmt.Errorf("sensitive label %q is given twice", s.Label)
		}
		seen[s.Label] = true
		if len(s.Globs) == 0 {
			return fmt.Errorf("sensitive label %q has no paths", s.Label)
		}
		if err := glob.Valid("sensitive", s.Globs); err != nil {
			return err
		}
	}
	return nil
}

// Touched is a label a pull request's changed paths matched, and the files it
// matched.
type Touched struct {
	Label string   `json:"label"`
	Files []string `json:"files"`
}

// Touches is each label in list that matched one of paths, in the operator's
// order, with the files it matched. Empty when none did.
func Touches(list []Path, paths []string) []Touched {
	var out []Touched
	for _, s := range list {
		if files := glob.Matching(s.Globs, paths); len(files) > 0 {
			out = append(out, Touched{Label: s.Label, Files: files})
		}
	}
	return out
}

// prefix begins the line.
const prefix = "**Sensitive:** "

// Line is the description's line for the labels touched: each with the files
// it matched. Empty when none were.
func Line(touched []Touched) string {
	return line(touched, func(t Touched) string {
		files := make([]string, len(t.Files))
		for i, p := range t.Files {
			files[i] = code(p)
		}
		return strings.Join(files, ", ")
	})
}

// Counted is Line with each label's count of files in place of the files,
// for a description the files would take over GitHub's limit.
func Counted(touched []Touched) string {
	return line(touched, func(t Touched) string {
		if len(t.Files) == 1 {
			return "1 file"
		}
		return fmt.Sprintf("%d files", len(t.Files))
	})
}

func line(touched []Touched, files func(Touched) string) string {
	if len(touched) == 0 {
		return ""
	}
	labels := make([]string, len(touched))
	for i, t := range touched {
		labels[i] = fmt.Sprintf("%s (%s)", t.Label, files(t))
	}
	return prefix + strings.Join(labels, ", ")
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

// Changed is the paths the pull request's diff changes: base to head, net, as
// the pull request shows them. A rename is both of its names.
//
// It is the diff from where head and base last met, which is the pull
// request's only while base is on the branch it merges into, at or past the
// commit head was last rebased onto. A branch rebased onto a newer tip than
// base would have the commits between them counted as its own.
func Changed(ctx context.Context, dir, base, head string) ([]string, error) {
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

// Reminder begins the line of the description that the sensitive line goes
// ahead of: the operator's review reminder, which ends Go's fixed part. The
// writer of a description begins that line with it.
const Reminder = "> **Your review**"

// With is a description with line as its sensitive line, in Go's fixed part
// ahead of the reminder, and everything from the reminder on as it was. An
// empty line removes the one there was. It is false for a description with
// no reminder to find, which the agent did not write.
func With(body, line string) (string, bool) {
	i := strings.Index(body, "\n"+Reminder)
	if i < 0 {
		return body, false
	}
	var head []string
	for _, l := range strings.Split(body[:i], "\n") {
		if !strings.HasPrefix(l, prefix) {
			head = append(head, l)
		}
	}
	top := strings.TrimRight(strings.Join(head, "\n"), "\n") + "\n\n"
	if line != "" {
		top += line + "\n\n"
	}
	return top + body[i+1:], true
}

// Strip is a description without its sensitive line, for a reader that is to
// be unaware of it. A description the agent did not write is as it was.
func Strip(body string) string {
	out, _ := With(body, "")
	return out
}
