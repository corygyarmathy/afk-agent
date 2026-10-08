package premise

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
)

// Dir is where in a workspace's .git the premises are written, and Index the
// file in it that lists them, which is written last.
const (
	Dir   = "afk-premises"
	Index = "index.md"
)

// Reader is how a premise's repository is read. *github.Client is one.
type Reader interface {
	Issue(ctx context.Context, n int) (github.Issue, error)
	Comments(ctx context.Context, n int) ([]github.Comment, error)
	DefaultBranch(ctx context.Context) (string, error)
	File(ctx context.Context, path, ref string) ([]byte, error)
}

// Fetch writes each link's source into dir, and then the index of them: a
// file permalink as the file at the linked revision and at the head of its
// repository's default branch, and an issue, a pull request or a comment as
// the thread is now. reader is how each repository is read; it is asked once
// for each.
//
// It returns how many links nothing could be fetched of. A link that cannot be
// fetched fails nothing: the index says why, and the session lists it as not
// verified. Only a write that fails is an error. dir is made afresh, and the
// index is written last, so a dir with no index is a fetch that did not finish.
func Fetch(ctx context.Context, dir string, links []Link, reader func(repo string) Reader) (int, error) {
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	readers := map[string]Reader{}
	in := func(repo string) Reader {
		key := strings.ToLower(repo)
		if readers[key] == nil {
			readers[key] = reader(repo)
		}
		return readers[key]
	}

	var b strings.Builder
	b.WriteString("# Premises\n\n")
	b.WriteString("The issue's Premises section links these. The agent fetched each as the work started: a file permalink at its linked revision and at the head of its repository's default branch, and an issue, a pull request or a comment as its thread is now. Each file named here is in this directory. A link that was not fetched is a premise whose source can't be read.\n\n")

	failed := 0
	for i, l := range links {
		fmt.Fprintf(&b, "%d. %s\n", i+1, l.Text)
		var lines []string
		var ok bool
		var err error
		switch {
		case l.Unread != "":
			lines = []string{"not fetched: " + l.Unread}
		case l.File():
			lines, ok, err = file(ctx, dir, i+1, l, in(l.Repo))
		default:
			lines, ok, err = thread(ctx, dir, i+1, l, in(l.Repo))
		}
		if err != nil {
			return 0, err
		}
		if !ok {
			failed++
		}
		for _, line := range lines {
			fmt.Fprintf(&b, "   - %s\n", line)
		}
	}
	return failed, os.WriteFile(filepath.Join(dir, Index), []byte(b.String()), 0o644)
}

// file fetches a file permalink at its revision and at the head of the
// default branch, and says what it wrote. ok is false only when neither could
// be read. A file gone from the head is worth saying, rather than a failure.
func file(ctx context.Context, dir string, i int, l Link, r Reader) (lines []string, ok bool, err error) {
	base := path.Base(l.Path)
	linked, lerr := r.File(ctx, l.Path, l.Ref)
	if lerr != nil {
		lines = append(lines, fmt.Sprintf("not fetched at the linked revision, `%s`: %v", l.Ref, lerr))
	} else {
		name := fmt.Sprintf("%d-linked-%s", i, base)
		if err := os.WriteFile(filepath.Join(dir, name), linked, 0o644); err != nil {
			return nil, false, err
		}
		lines = append(lines, fmt.Sprintf("`%s` at the linked revision, `%s`: `%s`", l.Path, l.Ref, name))
	}

	branch, herr := r.DefaultBranch(ctx)
	if herr != nil {
		lines = append(lines, fmt.Sprintf("not fetched at the head of the default branch, which could not be read: %v", herr))
		return lines, lerr == nil, nil
	}
	head, herr := r.File(ctx, l.Path, branch)
	if herr != nil {
		lines = append(lines, fmt.Sprintf("not fetched at the head of `%s`, the default branch: %v", branch, herr))
		return lines, lerr == nil, nil
	}
	name := fmt.Sprintf("%d-head-%s", i, base)
	if err := os.WriteFile(filepath.Join(dir, name), head, 0o644); err != nil {
		return nil, false, err
	}
	line := fmt.Sprintf("`%s` at the head of `%s`, the default branch: `%s`", l.Path, branch, name)
	switch {
	case lerr != nil:
	case bytes.Equal(linked, head):
		line += ", the same as at the linked revision"
	default:
		line += ", which differs from the linked revision"
	}
	return append(lines, line), true, nil
}

// thread fetches an issue's or a pull request's conversation as it is now: its
// title, its state, its description and every comment, the one a comment link
// points at marked.
func thread(ctx context.Context, dir string, i int, l Link, r Reader) (lines []string, ok bool, err error) {
	is, ierr := r.Issue(ctx, l.Number)
	if ierr != nil {
		return []string{fmt.Sprintf("not fetched: %v", ierr)}, false, nil
	}
	comments, cerr := r.Comments(ctx, l.Number)
	if cerr != nil {
		return []string{fmt.Sprintf("not fetched: its comments could not be read: %v", cerr)}, false, nil
	}

	kind := "an issue"
	if is.PullRequest {
		kind = "a pull request"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s#%d: %s\n\n%s, %s. Its conversation as it is now: the description, then every comment, oldest first. Review comments on a pull request's diff are not in it.\n\n", l.Repo, is.Number, is.Title, kind, is.State)
	if strings.TrimSpace(is.Body) == "" {
		b.WriteString("(no description)\n")
	} else {
		fmt.Fprintf(&b, "%s\n", is.Body)
	}
	found := false
	for _, c := range comments {
		mark := ""
		if l.Comment != 0 && c.ID == l.Comment {
			mark, found = " (the comment the premise links to)", true
		}
		fmt.Fprintf(&b, "\n---\n\n**%s, %s**%s\n\n%s\n", c.Login, c.CreatedAt.UTC().Format(time.RFC3339), mark, c.Body)
	}
	name := fmt.Sprintf("%d-thread.md", i)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		return nil, false, err
	}
	line := fmt.Sprintf("%s#%d, %s, %s, as its thread is now: `%s`", l.Repo, is.Number, kind, is.State, name)
	switch {
	case found:
		line += ", with the comment the link points at marked"
	case l.Comment != 0:
		line += ". The comment the link points at is not in it: deleted, or not on the conversation"
	}
	return []string{line}, true, nil
}
