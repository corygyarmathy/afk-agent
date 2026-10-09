// Package githubtest is a tracker the tests of the kinds that write to a
// branch share: pull requests, issues, their comments, reactions, labels and
// reviews, in memory, with what a test scripts going wrong; and File, the same
// tracker kept in a file, for the kill tests, so what a killed process did to
// it outlives the process the way GitHub would.
//
// It serves the methods of *github.Client those kinds call, and no more: a
// method is added when a test needs it, as package github's are.
//
// A test reads and arranges the tracker through its exported State, and
// scripts it through two hooks: Fail makes a call fail without landing, and
// Drop makes a write land and never be seen. State is plain data, so File
// keeps it as JSON.
package githubtest

import (
	"context"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/corygyarmathy/afk-agent/internal/github"
)

// Call is one call on the tracker, as Fail, Drop and File's hooks see it.
type Call struct {
	// Method is the *github.Client method called, such as "Comment".
	Method string

	// Number is the issue or pull request the call is on, ID the comment,
	// review or check-run subject it names, and NodeID the review's node.
	// Each is zero where the method takes none.
	Number int
	ID     int64
	NodeID string

	// Text is the call's text: a comment's body, a description, a label, a
	// reaction's content, a commit, or a compare's base...head.
	Text string
}

// State is everything on the tracker: what a test arranges before a call, and
// reads back after one.
type State struct {
	// PullRequests and Issues are the pull requests and issues, by number. A
	// pull request is an issue too, as GitHub has it: Issue and OpenIssues
	// read one from here without it being in Issues.
	PullRequests map[int]*github.PullRequest
	Issues       map[int]*github.Issue

	// CommentsOn is the comments on each issue and pull request, by number,
	// and ReactionsOn the reactions on each comment, by its id.
	CommentsOn  map[int][]github.Comment
	ReactionsOn map[int64][]github.Reaction

	// IssueReactionsOn is the reactions on each issue's or pull request's own
	// description, by number.
	IssueReactionsOn map[int][]github.Reaction

	// ReviewsOn is the submitted reviews on each pull request, by number,
	// LineCommentsOn each review's line comments, by its id, and
	// ReviewReactionsOn the reactions on each review, by its node id. A
	// review or line comment with no commit is read as written on the pull
	// request's head.
	ReviewsOn         map[int][]github.PullRequestReview
	LineCommentsOn    map[int64][]github.LineComment
	ReviewReactionsOn map[string][]github.Reaction

	// Required is the checks the base branch's rules require.
	Required []string

	// NextID is the last id given to a comment the agent wrote.
	NextID int64

	// Asks is how many times check runs have been read.
	Asks int

	// Writes is every write made, by its method, whether or not it landed.
	// Events is every write that landed and is seen, in order, as its method
	// and what it named: "Comment 1001", "Label ready-for-review". Reacts is
	// how many reactions landed on each comment, including one GitHub
	// already had.
	Writes map[string]int
	Events []string
	Reacts map[int64]int

	// Compares is every diff read between two commits, as base...head.
	Compares []string
}

// Tracker is the tracker in memory. New makes one.
type Tracker struct {
	mu sync.Mutex
	State

	// Login is the agent's account: whose the comments and reactions it
	// writes are.
	Login string

	// Checks is the check runs on a commit, by the time they are asked
	// about. Nil is none.
	Checks func(sha string, call int) []github.CheckRun

	// Live is a bare remote whose branches are the pull requests' heads,
	// which then move with a push as GitHub's do. Empty is each pull
	// request's HeadSHA, fixed.
	Live string

	// Fail is asked before every call, read or write. An error is what the
	// call fails with, without landing. Nil fails nothing.
	Fail func(Call) error

	// Drop is asked of every write that would land. True is a write GitHub
	// took and never shows: it succeeds, and nothing reads it back. Nil
	// drops nothing.
	Drop func(Call) bool
}

// New is an empty tracker on which login is the agent.
func New(login string) *Tracker {
	tr := &Tracker{Login: login, State: State{NextID: 1000}}
	tr.fill()
	return tr
}

// fill makes every map in the state that is nil an empty one, so a write can
// add to it.
func (s *State) fill() {
	if s.PullRequests == nil {
		s.PullRequests = map[int]*github.PullRequest{}
	}
	if s.Issues == nil {
		s.Issues = map[int]*github.Issue{}
	}
	if s.CommentsOn == nil {
		s.CommentsOn = map[int][]github.Comment{}
	}
	if s.ReactionsOn == nil {
		s.ReactionsOn = map[int64][]github.Reaction{}
	}
	if s.IssueReactionsOn == nil {
		s.IssueReactionsOn = map[int][]github.Reaction{}
	}
	if s.ReviewsOn == nil {
		s.ReviewsOn = map[int][]github.PullRequestReview{}
	}
	if s.LineCommentsOn == nil {
		s.LineCommentsOn = map[int64][]github.LineComment{}
	}
	if s.ReviewReactionsOn == nil {
		s.ReviewReactionsOn = map[string][]github.Reaction{}
	}
	if s.Writes == nil {
		s.Writes = map[string]int{}
	}
	if s.Reacts == nil {
		s.Reacts = map[int64]int{}
	}
}

// Say posts c on issue or pull request n, as whoever c says wrote it.
func (tr *Tracker) Say(n int, c github.Comment) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.CommentsOn[n] = append(tr.CommentsOn[n], c)
}

// notFound is GitHub's answer for a subject or comment that is not there.
func notFound(c Call) error {
	return &github.StatusError{Method: c.Method, Code: 404, Status: "404 Not Found"}
}

// read is asked before a read: Fail's answer.
func (tr *Tracker) read(c Call) error {
	if tr.Fail != nil {
		return tr.Fail(c)
	}
	return nil
}

// write counts a write and asks Fail and Drop of it. It is true when the write
// is to land and be seen; a non-nil error is the write failing.
func (tr *Tracker) write(c Call) (bool, error) {
	tr.Writes[c.Method]++
	if tr.Fail != nil {
		if err := tr.Fail(c); err != nil {
			return false, err
		}
	}
	return tr.Drop == nil || !tr.Drop(c), nil
}

// landed records a write that landed.
func (tr *Tracker) landed(c Call, what any) {
	tr.Events = append(tr.Events, fmt.Sprintf("%s %v", c.Method, what))
}

func (tr *Tracker) PullRequest(_ context.Context, n int) (github.PullRequest, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "PullRequest", Number: n}
	if err := tr.read(c); err != nil {
		return github.PullRequest{}, err
	}
	pr, ok := tr.pullRequest(n)
	if !ok {
		return github.PullRequest{}, notFound(c)
	}
	return pr, nil
}

// pullRequest is pull request n as GitHub serves it: a copy, with its head the
// live remote's when there is one.
func (tr *Tracker) pullRequest(n int) (github.PullRequest, bool) {
	p, ok := tr.PullRequests[n]
	if !ok {
		return github.PullRequest{}, false
	}
	pr := *p
	pr.Labels = append([]string(nil), p.Labels...)
	if tr.Live != "" {
		out, err := exec.Command("git", "-C", tr.Live, "rev-parse", "refs/heads/"+pr.HeadRef).Output()
		if err == nil {
			pr.HeadSHA = strings.TrimSpace(string(out))
		}
	}
	return pr, true
}

// asIssue is pull request pr as the issues API serves it.
func asIssue(pr github.PullRequest) github.Issue {
	return github.Issue{Number: pr.Number, State: pr.State, Title: pr.Title, PullRequest: true, Labels: pr.Labels, Author: pr.Login}
}

func (tr *Tracker) Issue(_ context.Context, n int) (github.Issue, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "Issue", Number: n}
	if err := tr.read(c); err != nil {
		return github.Issue{}, err
	}
	if pr, ok := tr.pullRequest(n); ok {
		return asIssue(pr), nil
	}
	is, ok := tr.Issues[n]
	if !ok {
		return github.Issue{}, notFound(c)
	}
	out := *is
	out.Labels = append([]string(nil), is.Labels...)
	return out, nil
}

// OpenIssues is the open issues and pull requests, in the order of their
// numbers.
func (tr *Tracker) OpenIssues(context.Context) ([]github.Issue, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "OpenIssues"}); err != nil {
		return nil, err
	}
	var out []github.Issue
	for _, n := range tr.numbers() {
		if pr, ok := tr.pullRequest(n); ok {
			if pr.State == "open" {
				out = append(out, asIssue(pr))
			}
		} else if is := tr.Issues[n]; is.State == "open" {
			out = append(out, *is)
		}
	}
	return out, nil
}

// numbers is every issue's and pull request's number, in order.
func (tr *Tracker) numbers() []int {
	ns := slices.Collect(maps.Keys(tr.PullRequests))
	for n := range tr.Issues {
		if _, ok := tr.PullRequests[n]; !ok {
			ns = append(ns, n)
		}
	}
	slices.Sort(ns)
	return ns
}

func (tr *Tracker) Comments(_ context.Context, n int) ([]github.Comment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "Comments", Number: n}); err != nil {
		return nil, err
	}
	return append([]github.Comment(nil), tr.CommentsOn[n]...), nil
}

func (tr *Tracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "Reactions", ID: id}); err != nil {
		return nil, err
	}
	return append([]github.Reaction(nil), tr.ReactionsOn[id]...), nil
}

func (tr *Tracker) IssueReactions(_ context.Context, n int) ([]github.Reaction, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "IssueReactions", Number: n}); err != nil {
		return nil, err
	}
	return append([]github.Reaction(nil), tr.IssueReactionsOn[n]...), nil
}

// Comment posts body on n as the agent. A dropped comment is given its id, and
// is never seen.
func (tr *Tracker) Comment(_ context.Context, n int, body string) (github.Comment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "Comment", Number: n, Text: body}
	lands, err := tr.write(c)
	if err != nil {
		return github.Comment{}, err
	}
	tr.NextID++
	cm := github.Comment{ID: tr.NextID, Login: tr.Login, Body: body}
	if lands {
		tr.CommentsOn[n] = append(tr.CommentsOn[n], cm)
		tr.landed(c, cm.ID)
	}
	return cm, nil
}

func (tr *Tracker) EditComment(_ context.Context, id int64, body string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "EditComment", ID: id, Text: body}
	lands, err := tr.write(c)
	if err != nil {
		return err
	}
	for n, cs := range tr.CommentsOn {
		for i := range cs {
			if cs[i].ID == id {
				if lands {
					tr.CommentsOn[n][i].Body = body
					tr.landed(c, id)
				}
				return nil
			}
		}
	}
	return notFound(c)
}

// react adds the agent's content to rs, unless the agent's is already there,
// as GitHub keeps one reaction of each content per account.
func (tr *Tracker) react(rs []github.Reaction, content string) []github.Reaction {
	for _, r := range rs {
		if r.Login == tr.Login && r.Content == content {
			return rs
		}
	}
	return append(rs, github.Reaction{Login: tr.Login, Content: content})
}

func (tr *Tracker) React(_ context.Context, id int64, content string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "React", ID: id, Text: content}
	lands, err := tr.write(c)
	if err != nil || !lands {
		return err
	}
	tr.Reacts[id]++
	tr.ReactionsOn[id] = tr.react(tr.ReactionsOn[id], content)
	tr.landed(c, id)
	return nil
}

func (tr *Tracker) ReactToIssue(_ context.Context, n int, content string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "ReactToIssue", Number: n, Text: content}
	lands, err := tr.write(c)
	if err != nil || !lands {
		return err
	}
	tr.IssueReactionsOn[n] = tr.react(tr.IssueReactionsOn[n], content)
	tr.landed(c, n)
	return nil
}

// labels is where n's labels are kept, or nil when there is no n.
func (tr *Tracker) labels(n int) *[]string {
	if pr, ok := tr.PullRequests[n]; ok {
		return &pr.Labels
	}
	if is, ok := tr.Issues[n]; ok {
		return &is.Labels
	}
	return nil
}

func (tr *Tracker) Label(_ context.Context, n int, label string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "Label", Number: n, Text: label}
	lands, err := tr.write(c)
	if err != nil {
		return err
	}
	ls := tr.labels(n)
	if ls == nil {
		return notFound(c)
	}
	if !lands {
		return nil
	}
	if !github.HasLabel(*ls, label) {
		*ls = append(*ls, label)
	}
	tr.landed(c, label)
	return nil
}

func (tr *Tracker) Unlabel(_ context.Context, n int, label string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "Unlabel", Number: n, Text: label}
	lands, err := tr.write(c)
	if err != nil {
		return err
	}
	ls := tr.labels(n)
	if ls == nil {
		return notFound(c)
	}
	if !lands {
		return nil
	}
	var kept []string
	for _, l := range *ls {
		if l != label {
			kept = append(kept, l)
		}
	}
	*ls = kept
	tr.landed(c, label)
	return nil
}

func (tr *Tracker) EditPullRequest(_ context.Context, n int, body string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "EditPullRequest", Number: n, Text: body}
	lands, err := tr.write(c)
	if err != nil {
		return err
	}
	pr, ok := tr.PullRequests[n]
	if !ok {
		return notFound(c)
	}
	if lands {
		pr.Body = body
		tr.landed(c, n)
	}
	return nil
}

func (tr *Tracker) CheckRuns(_ context.Context, sha string) ([]github.CheckRun, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "CheckRuns", Text: sha}); err != nil {
		return nil, err
	}
	tr.Asks++
	if tr.Checks == nil {
		return nil, nil
	}
	return tr.Checks(sha, tr.Asks), nil
}

func (tr *Tracker) RequiredChecks(_ context.Context, branch string) ([]string, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "RequiredChecks", Text: branch}); err != nil {
		return nil, err
	}
	return append([]string(nil), tr.Required...), nil
}

// Compare records the diff read, and serves an empty one.
func (tr *Tracker) Compare(_ context.Context, base, head string) (string, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "Compare", Text: base + "..." + head}
	if err := tr.read(c); err != nil {
		return "", err
	}
	tr.Compares = append(tr.Compares, c.Text)
	return "", nil
}

// head is pull request n's head, as PullRequest reads it.
func (tr *Tracker) head(n int) string {
	pr, _ := tr.pullRequest(n)
	return pr.HeadSHA
}

func (tr *Tracker) PullRequestReviews(_ context.Context, n int) ([]github.PullRequestReview, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "PullRequestReviews", Number: n}); err != nil {
		return nil, err
	}
	out := append([]github.PullRequestReview(nil), tr.ReviewsOn[n]...)
	for i := range out {
		if out[i].CommitID == "" {
			out[i].CommitID = tr.head(n)
		}
	}
	return out, nil
}

func (tr *Tracker) LineComments(_ context.Context, n int, review int64) ([]github.LineComment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "LineComments", Number: n, ID: review}); err != nil {
		return nil, err
	}
	out := append([]github.LineComment(nil), tr.LineCommentsOn[review]...)
	for i := range out {
		if out[i].CommitID == "" {
			out[i].CommitID = tr.head(n)
		}
	}
	return out, nil
}

func (tr *Tracker) PullRequestReviewReactions(_ context.Context, nodeID string) ([]github.Reaction, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err := tr.read(Call{Method: "PullRequestReviewReactions", NodeID: nodeID}); err != nil {
		return nil, err
	}
	return append([]github.Reaction(nil), tr.ReviewReactionsOn[nodeID]...), nil
}

func (tr *Tracker) ReactToPullRequestReview(_ context.Context, nodeID, content string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := Call{Method: "ReactToPullRequestReview", NodeID: nodeID, Text: content}
	lands, err := tr.write(c)
	if err != nil || !lands {
		return err
	}
	tr.ReviewReactionsOn[nodeID] = tr.react(tr.ReviewReactionsOn[nodeID], content)
	tr.landed(c, nodeID)
	return nil
}
