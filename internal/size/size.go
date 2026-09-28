// Package size measures a diff against the size signal: how many changed lines
// a reviewer would read, with the tests counted beside them (#107, #126).
//
// It is a signal, not a gate. Crossing it asks for a decision - hand the work
// back, or cut it - rather than failing anything, and what the decision is
// belongs to the caller. `/implement` measures the work it is about to open a
// pull request for, and `/revise` the whole pull request after a revision.
//
// The count is made by git on the commits, never taken from a session's word,
// and it reads nothing a checkout could change: it runs in a bare repository as
// well as in a clone, and the attributes it honours are the ones committed at
// the head being measured.
package size

import (
	"context"
	"errors"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/corygyarmathy/afk-agent/internal/git"
)

// Count is a diff's size.
type Count struct {
	// Lines is the changed lines that are not tests: each line added and
	// each line removed.
	Lines int

	// Tests is the changed lines of tests, reported beside Lines rather than
	// left out unsaid.
	Tests int
}

// Over reports whether the count crosses signal, the changed non-test lines
// one pull request may have before it needs a decision. Tests do not count
// towards it.
func (c Count) Over(signal int) bool { return c.Lines > signal }

// Measure is the size of the diff from base to head in the repository at dir,
// which may be bare.
//
// Left out of the count altogether:
//
//   - a file deleted whole: removing code is not what a review is spent on;
//   - vendored files: under a `vendor`, `third_party` or `node_modules`
//     directory, or marked `linguist-vendored`;
//   - generated files: lock files by their names, a file with Go's
//     `// Code generated ... DO NOT EDIT.` line, or one marked
//     `linguist-generated`;
//   - binary files, which have no lines to read.
//
// An attribute set false in the head's `.gitattributes` takes back what a
// convention decided: `vendor/ours/** -linguist-vendored` counts that
// directory. A rename is counted by what changed in it, so a file only moved
// counts nothing.
func Measure(ctx context.Context, dir, base, head string) (Count, error) {
	files, err := changed(ctx, dir, base, head)
	if err != nil || len(files) == 0 {
		return Count{}, err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	attrs, err := attributes(ctx, dir, head, paths)
	if err != nil {
		return Count{}, err
	}

	var kept []file
	var unknown []string
	for _, f := range files {
		a := attrs[f.path]
		if excluded(a.vendored, vendored(f.path)) || excluded(a.generated, lock(f.path)) {
			continue
		}
		kept = append(kept, f)
		if a.generated == "" {
			unknown = append(unknown, f.path)
		}
	}
	marked, err := generatedByHeader(ctx, dir, head, unknown)
	if err != nil {
		return Count{}, err
	}

	var c Count
	for _, f := range kept {
		switch {
		case marked[f.path]:
		case test(f.path):
			c.Tests += f.lines
		default:
			c.Lines += f.lines
		}
	}
	return c, nil
}

// file is one path the diff changes, by its name at the head, and how many
// lines of it changed.
type file struct {
	path  string
	lines int
}

// changed is every file the diff from base to head changes, except those it
// deletes whole. It is diffed with renames found, so a moved file is one entry
// under its new name. Nothing it runs reads the agent user's configuration,
// and no external diff or text conversion is run.
func changed(ctx context.Context, dir, base, head string) ([]file, error) {
	diff := []string{"diff", "--no-ext-diff", "--no-textconv", "--find-renames", "-z"}
	deleted, err := git.Output(ctx, dir, git.Isolated, append(diff, "--name-only", "--diff-filter=D", base, head)...)
	if err != nil {
		return nil, err
	}
	gone := map[string]bool{}
	for _, p := range strings.Split(deleted, "\x00") {
		gone[p] = true
	}
	out, err := git.Output(ctx, dir, git.Isolated, append(diff, "--numstat", base, head)...)
	if err != nil {
		return nil, err
	}

	// Each entry is `added<TAB>removed<TAB>path<NUL>`, or, for a rename,
	// `added<TAB>removed<TAB><NUL>old<NUL>new<NUL>`. A binary file's counts
	// are `-`.
	var files []file
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		stat := strings.SplitN(fields[i], "\t", 3)
		if len(stat) != 3 {
			continue
		}
		name := stat[2]
		if name == "" {
			if i+2 >= len(fields) {
				break
			}
			name, i = fields[i+2], i+2
		}
		if gone[name] {
			continue
		}
		added, _ := strconv.Atoi(stat[0])
		removed, _ := strconv.Atoi(stat[1])
		files = append(files, file{path: name, lines: added + removed})
	}
	return files, nil
}

// attrs is what a file's attributes say about it: "set" or "true" includes it
// in the category, "unset" or "false" takes it out, and empty says nothing.
type attrs struct{ vendored, generated string }

// attributes reads the linguist attributes of paths from the .gitattributes
// committed at head, never from a checkout.
func attributes(ctx context.Context, dir, head string, paths []string) (map[string]attrs, error) {
	out, err := git.Output(ctx, dir, git.Isolated, append([]string{"check-attr", "-z", "--source=" + head, "linguist-vendored", "linguist-generated", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	got := map[string]attrs{}
	fields := strings.Split(out, "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		p, name, value := fields[i], fields[i+1], fields[i+2]
		switch value {
		case "set", "true":
			value = "set"
		case "unset", "false":
			value = "unset"
		default:
			value = ""
		}
		a := got[p]
		if name == "linguist-vendored" {
			a.vendored = value
		} else {
			a.generated = value
		}
		got[p] = a
	}
	return got, nil
}

// excluded is whether a category leaves a file out: its attribute if it says
// either way, and the convention if it does not.
func excluded(attribute string, convention bool) bool {
	if attribute != "" {
		return attribute == "set"
	}
	return convention
}

// generatedHeader is Go's convention for a generated file (go help generate).
const generatedHeader = `^// Code generated .* DO NOT EDIT\.$`

// generatedByHeader is which of paths carry the generated header at head.
func generatedByHeader(ctx context.Context, dir, head string, paths []string) (map[string]bool, error) {
	marked := map[string]bool{}
	if len(paths) == 0 {
		return marked, nil
	}
	out, err := git.Output(ctx, dir, git.Isolated, append([]string{"--literal-pathspecs", "grep", "-z", "-l", "-I", "-E", generatedHeader, head, "--"}, paths...)...)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		// Nothing matched.
		return marked, nil
	}
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			marked[strings.TrimPrefix(p, head+":")] = true
		}
	}
	return marked, nil
}

// vendored is whether a path is under a directory that by convention holds
// someone else's code.
func vendored(p string) bool {
	for _, dir := range strings.Split(path.Dir(p), "/") {
		switch dir {
		case "vendor", "third_party", "node_modules":
			return true
		}
	}
	return false
}

// lock is whether a path is, by its name, a lock file: generated by a package
// manager, and read by nobody.
func lock(p string) bool {
	base := path.Base(p)
	switch base {
	case "go.sum", "go.work.sum", "Package.resolved", "npm-shrinkwrap.json":
		return true
	}
	for _, suffix := range []string{".lock", ".lockb", ".lock.json", "-lock.json", "-lock.yaml"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// test is whether a path is, by convention, a test or a test's data.
func test(p string) bool {
	for _, dir := range strings.Split(path.Dir(p), "/") {
		switch dir {
		case "test", "tests", "testdata", "__tests__", "spec":
			return true
		}
	}
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	return strings.HasSuffix(stem, "_test") || strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") ||
		strings.HasSuffix(stem, "_spec") || (strings.HasPrefix(base, "test_") && path.Ext(base) == ".py")
}
