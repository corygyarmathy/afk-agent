package githubtest

import (
	"context"
	"encoding/json"
	"os"

	"github.com/corygyarmathy/afk-agent/internal/github"
)

// File is a Tracker kept in a file, for a kill test: every call loads the
// state, makes the call on a Tracker holding it, and saves what the call left,
// so that what a process killed dead did to the tracker survives it the way
// GitHub would.
type File struct {
	Path string

	// New is the tracker each call is made on before the file's state is put
	// in it: its login, checks, live remote and hooks. Nil is New(""), which
	// is the agent with no login.
	New func() *Tracker

	// Before is called before a call loads anything, and After once the call
	// has landed in the file, before the caller hears back. A kill test dies
	// in one of them. Nil does nothing.
	Before func(Call)
	After  func(Call)
}

// Load is the tracker as the file has it.
func (f *File) Load() (*Tracker, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	tr := f.tracker()
	if err := json.Unmarshal(b, &tr.State); err != nil {
		return nil, err
	}
	tr.fill()
	return tr, nil
}

// tracker is an empty tracker, made as New says.
func (f *File) tracker() *Tracker {
	if f.New == nil {
		return New("")
	}
	return f.New()
}

// Save writes tr's state to the file, replacing it whole.
func (f *File) Save(tr *Tracker) error {
	tr.mu.Lock()
	b, err := json.Marshal(tr.State)
	tr.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// do makes call c on the tracker the file has, by do, and saves what it left.
func (f *File) do(c Call, do func(tr *Tracker) error) error {
	if f.Before != nil {
		f.Before(c)
	}
	tr, err := f.Load()
	if err != nil {
		return err
	}
	callErr := do(tr)
	if err := f.Save(tr); err != nil {
		return err
	}
	if callErr == nil && f.After != nil {
		f.After(c)
	}
	return callErr
}

func (f *File) PullRequest(ctx context.Context, n int) (pr github.PullRequest, err error) {
	err = f.do(Call{Method: "PullRequest", Number: n}, func(tr *Tracker) (err error) { pr, err = tr.PullRequest(ctx, n); return err })
	return pr, err
}

func (f *File) Issue(ctx context.Context, n int) (is github.Issue, err error) {
	err = f.do(Call{Method: "Issue", Number: n}, func(tr *Tracker) (err error) { is, err = tr.Issue(ctx, n); return err })
	return is, err
}

func (f *File) OpenIssues(ctx context.Context) (is []github.Issue, err error) {
	err = f.do(Call{Method: "OpenIssues"}, func(tr *Tracker) (err error) { is, err = tr.OpenIssues(ctx); return err })
	return is, err
}

func (f *File) Comments(ctx context.Context, n int) (cs []github.Comment, err error) {
	err = f.do(Call{Method: "Comments", Number: n}, func(tr *Tracker) (err error) { cs, err = tr.Comments(ctx, n); return err })
	return cs, err
}

func (f *File) Reactions(ctx context.Context, id int64) (rs []github.Reaction, err error) {
	err = f.do(Call{Method: "Reactions", ID: id}, func(tr *Tracker) (err error) { rs, err = tr.Reactions(ctx, id); return err })
	return rs, err
}

func (f *File) IssueReactions(ctx context.Context, n int) (rs []github.Reaction, err error) {
	err = f.do(Call{Method: "IssueReactions", Number: n}, func(tr *Tracker) (err error) { rs, err = tr.IssueReactions(ctx, n); return err })
	return rs, err
}

func (f *File) Comment(ctx context.Context, n int, body string) (c github.Comment, err error) {
	err = f.do(Call{Method: "Comment", Number: n, Text: body}, func(tr *Tracker) (err error) { c, err = tr.Comment(ctx, n, body); return err })
	return c, err
}

func (f *File) EditComment(ctx context.Context, id int64, body string) error {
	return f.do(Call{Method: "EditComment", ID: id, Text: body}, func(tr *Tracker) error { return tr.EditComment(ctx, id, body) })
}

func (f *File) React(ctx context.Context, id int64, content string) error {
	return f.do(Call{Method: "React", ID: id, Text: content}, func(tr *Tracker) error { return tr.React(ctx, id, content) })
}

func (f *File) ReactToIssue(ctx context.Context, n int, content string) error {
	return f.do(Call{Method: "ReactToIssue", Number: n, Text: content}, func(tr *Tracker) error { return tr.ReactToIssue(ctx, n, content) })
}

func (f *File) Label(ctx context.Context, n int, label string) error {
	return f.do(Call{Method: "Label", Number: n, Text: label}, func(tr *Tracker) error { return tr.Label(ctx, n, label) })
}

func (f *File) Unlabel(ctx context.Context, n int, label string) error {
	return f.do(Call{Method: "Unlabel", Number: n, Text: label}, func(tr *Tracker) error { return tr.Unlabel(ctx, n, label) })
}

func (f *File) EditPullRequest(ctx context.Context, n int, body string) error {
	return f.do(Call{Method: "EditPullRequest", Number: n, Text: body}, func(tr *Tracker) error { return tr.EditPullRequest(ctx, n, body) })
}

func (f *File) CheckRuns(ctx context.Context, sha string) (rs []github.CheckRun, err error) {
	err = f.do(Call{Method: "CheckRuns", Text: sha}, func(tr *Tracker) (err error) { rs, err = tr.CheckRuns(ctx, sha); return err })
	return rs, err
}

func (f *File) RequiredChecks(ctx context.Context, branch string) (cs []string, err error) {
	err = f.do(Call{Method: "RequiredChecks", Text: branch}, func(tr *Tracker) (err error) { cs, err = tr.RequiredChecks(ctx, branch); return err })
	return cs, err
}

func (f *File) Compare(ctx context.Context, base, head string) (d string, err error) {
	err = f.do(Call{Method: "Compare", Text: base + "..." + head}, func(tr *Tracker) (err error) { d, err = tr.Compare(ctx, base, head); return err })
	return d, err
}

func (f *File) PullRequestReviews(ctx context.Context, n int) (rs []github.PullRequestReview, err error) {
	err = f.do(Call{Method: "PullRequestReviews", Number: n}, func(tr *Tracker) (err error) { rs, err = tr.PullRequestReviews(ctx, n); return err })
	return rs, err
}

func (f *File) LineComments(ctx context.Context, n int, review int64) (ls []github.LineComment, err error) {
	err = f.do(Call{Method: "LineComments", Number: n, ID: review}, func(tr *Tracker) (err error) { ls, err = tr.LineComments(ctx, n, review); return err })
	return ls, err
}

func (f *File) PullRequestReviewReactions(ctx context.Context, nodeID string) (rs []github.Reaction, err error) {
	err = f.do(Call{Method: "PullRequestReviewReactions", NodeID: nodeID}, func(tr *Tracker) (err error) { rs, err = tr.PullRequestReviewReactions(ctx, nodeID); return err })
	return rs, err
}

func (f *File) ReactToPullRequestReview(ctx context.Context, nodeID, content string) error {
	return f.do(Call{Method: "ReactToPullRequestReview", NodeID: nodeID, Text: content}, func(tr *Tracker) error { return tr.ReactToPullRequestReview(ctx, nodeID, content) })
}
