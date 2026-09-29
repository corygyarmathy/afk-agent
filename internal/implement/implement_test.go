package implement_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/handoff"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

const (
	agent  = "afk-bot"
	prefix = "afk/"
	issue  = 7
)

var now = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// tracker is one issue on a fixture tracker, and the pull requests open beside
// it.
type tracker struct {
	mu     sync.Mutex
	state  string
	body   string
	labels []string
	opened []github.NewPullRequest

	// commentedOn and labelledOn are the numbers each comment and label
	// went on, in order.
	commentedOn []int
	labelledOn  []int

	// repoLabels is the labels the repository already has. Applying one of
	// them in another case keeps the repository's spelling, as GitHub does.
	repoLabels []string

	// checks is the check runs on a commit, by the time they are asked
	// for: none, unless a test says otherwise.
	checks func(sha string, call int) []github.CheckRun
	asks   int

	// required is the checks the base branch's rules require: none, unless
	// a test says otherwise.
	required []string

	// open decides what happens to a pull request the agent opens: whether
	// it is opened, and what the call reports.
	open        func(call int) (opens bool, err error)
	opens       int
	pullRequest bool
	prs         []github.PullRequest
	comments    []github.Comment
	reactions   map[int64][]github.Reaction
	nextID      int64

	// eyes is the reactions on the issue itself.
	eyes []github.Reaction

	// failComments is how many of the agent's next comments fail, without
	// landing.
	failComments int

	// failIssues is how many of the next issue reads fail.
	failIssues int

	// failEdits is how many of the next edits of a pull request's
	// description fail, without landing.
	failEdits int

	// filed is the issues the agent filed, and blocked the issues' native
	// blockers, by number.
	filed   []github.Issue
	blocked map[int][]int

	// closeFiled is a human closing each issue the agent files as soon as
	// it is filed.
	closeFiled bool

	// failBlocks is how many of the next dependencies added fail, without
	// landing.
	failBlocks int
}

// issueID is the id of the issue the job is for, which is not its number.
const issueID = 7007

var (
	errEdit  = errors.New("PATCH pull request: 502 Bad Gateway")
	errBlock = errors.New("POST blocked_by: 502 Bad Gateway")
)

func newTracker(comments ...github.Comment) *tracker {
	return &tracker{state: "open", comments: comments, reactions: map[int64][]github.Reaction{}, nextID: 1000}
}

func (tr *tracker) Issue(_ context.Context, n int) (github.Issue, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.failIssues > 0 {
		tr.failIssues--
		return github.Issue{}, &github.StatusError{Method: "GET", URL: "/issues/7", Code: 502, Status: "502 Bad Gateway"}
	}
	var labels []string
	for i, on := range tr.labelledOn {
		if on == n {
			labels = append(labels, tr.labels[i])
		}
	}
	for _, is := range tr.filed {
		if is.Number == n {
			is.Labels = labels
			return is, nil
		}
	}
	return github.Issue{Number: n, ID: issueID, State: tr.state, Title: "Reserve a job", Body: tr.body, PullRequest: tr.pullRequest, Labels: labels}, nil
}

// IssuesBy is the issues the agent filed, open and closed: the issue the job
// is for is not one of them.
func (tr *tracker) IssuesBy(_ context.Context, login string) ([]github.Issue, error) {
	if login != agent {
		return nil, errors.New("GET issues: not the agent's")
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]github.Issue(nil), tr.filed...), nil
}

func (tr *tracker) CreateIssue(_ context.Context, is github.NewIssue) (github.Issue, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 200 + len(tr.filed) + 1
	filed := github.Issue{Number: n, ID: int64(n) * 1000, State: "open", Title: is.Title, Body: is.Body, Author: agent}
	if tr.closeFiled {
		filed.State = "closed"
	}
	tr.filed = append(tr.filed, filed)
	return filed, nil
}

func (tr *tracker) BlockedBy(_ context.Context, n int) ([]github.Issue, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []github.Issue
	for _, b := range tr.blocked[n] {
		out = append(out, github.Issue{Number: b})
	}
	return out, nil
}

func (tr *tracker) AddBlockedBy(_ context.Context, n int, blocker int64) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if blocker != issueID {
		return errors.New("POST blocked_by: 404 Not Found")
	}
	if tr.failBlocks > 0 {
		tr.failBlocks--
		return errBlock
	}
	if tr.blocked == nil {
		tr.blocked = map[int][]int{}
	}
	for _, b := range tr.blocked[n] {
		if b == issue {
			return errors.New("POST blocked_by: 422 Unprocessable Entity")
		}
	}
	tr.blocked[n] = append(tr.blocked[n], issue)
	return nil
}

func (tr *tracker) OpenPullRequests(context.Context) ([]github.PullRequest, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]github.PullRequest(nil), tr.prs...), nil
}

func (tr *tracker) Comments(context.Context, int) ([]github.Comment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]github.Comment(nil), tr.comments...), nil
}

func (tr *tracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]github.Reaction(nil), tr.reactions[id]...), nil
}

func (tr *tracker) Comment(_ context.Context, n int, body string) (github.Comment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.failComments > 0 {
		tr.failComments--
		return github.Comment{}, errors.New("POST comment: 502 Bad Gateway")
	}
	tr.commentedOn = append(tr.commentedOn, n)
	tr.nextID++
	c := github.Comment{ID: tr.nextID, Login: agent, Association: "NONE", Body: body}
	tr.comments = append(tr.comments, c)
	return c, nil
}

func (tr *tracker) React(_ context.Context, id int64, content string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, r := range tr.reactions[id] {
		if r.Login == agent && r.Content == content {
			return nil
		}
	}
	tr.reactions[id] = append(tr.reactions[id], github.Reaction{Login: agent, Content: content})
	return nil
}

// Only the issue is claimed this way. A pull request's description is the
// review's to claim.
func (tr *tracker) IssueReactions(_ context.Context, n int) ([]github.Reaction, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if n != issue {
		return nil, nil
	}
	return append([]github.Reaction(nil), tr.eyes...), nil
}

func (tr *tracker) ReactToIssue(_ context.Context, n int, content string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if n != issue {
		return errors.New("the implement kind never claims a description")
	}
	for _, r := range tr.eyes {
		if r.Login == agent && r.Content == content {
			return nil
		}
	}
	tr.eyes = append(tr.eyes, github.Reaction{Login: agent, Content: content})
	return nil
}

func (tr *tracker) Unlabel(context.Context, int, string) error {
	return errors.New("the implement kind never takes a label off")
}

func (tr *tracker) Label(_ context.Context, n int, label string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.labelledOn = append(tr.labelledOn, n)
	for _, l := range tr.repoLabels {
		if strings.EqualFold(l, label) {
			label = l
		}
	}
	for i := range tr.prs {
		if tr.prs[i].Number == n {
			tr.prs[i].Labels = append(tr.prs[i].Labels, label)
		}
	}
	tr.labels = append(tr.labels, label)
	return nil
}

func (tr *tracker) CheckRuns(_ context.Context, sha string) ([]github.CheckRun, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.asks++
	if tr.checks == nil {
		return nil, nil
	}
	return tr.checks(sha, tr.asks), nil
}

func (tr *tracker) RequiredChecks(context.Context, string) ([]string, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.required, nil
}

func (tr *tracker) CreatePullRequest(_ context.Context, req github.NewPullRequest) (github.PullRequest, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.opens++
	opens, err := true, error(nil)
	if tr.open != nil {
		opens, err = tr.open(tr.opens)
	}
	pr := github.PullRequest{Number: 100 + tr.opens, State: "open", HeadRef: req.Head, Login: agent, Title: req.Title, Body: req.Body}
	if opens {
		tr.opened = append(tr.opened, req)
		tr.prs = append(tr.prs, pr)
	}
	return pr, err
}

func (tr *tracker) EditPullRequest(_ context.Context, n int, body string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.failEdits > 0 {
		tr.failEdits--
		return errEdit
	}
	for i := range tr.prs {
		if tr.prs[i].Number == n {
			tr.prs[i].Body = body
			return nil
		}
	}
	return errors.New("PATCH pull request: 404 Not Found")
}

// byAgent is the comments the agent wrote.
func (tr *tracker) byAgent() []github.Comment {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []github.Comment
	for _, c := range tr.comments {
		if c.Login == agent {
			out = append(out, c)
		}
	}
	return out
}

// claims is how many claims the agent has made on a comment.
func (tr *tracker) claims(id int64) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, r := range tr.reactions[id] {
		if r.Login == agent && r.Content == intake.Claim {
			n++
		}
	}
	return n
}

type fixture struct {
	t      *testing.T
	store  store.Store
	tr     *tracker
	model  *coder
	deps   *implement.Deps
	remote string
	reg    *transition.Registry
	run    *transition.Runner
	job    store.Job

	// at is the time the runner sees: now, until a test moves it on.
	at time.Time

	// logged is every line the deps logged.
	logged []string

	// last and lastErr are what drive's last run returned: an error with
	// Parked is the outcome dispatch tells the operator about.
	last    transition.Outcome
	lastErr error
}

// setup is an issue on a fixture tracker, a repository with one commit on a
// local bare remote, a model that does nothing until a test says what, and a
// gate that passes while the workspace has a file called `ok`.
func setup(t *testing.T, tr *tracker) *fixture {
	t.Helper()
	s := storetest.Open(t)
	remote := bareRemote(t)
	m := &coder{}
	d := &implement.Deps{
		Tracker:       tr,
		Model:         m,
		Login:         agent,
		BranchPrefix:  prefix,
		Remote:        git.Remote{URL: remote},
		Resolve:       func(context.Context) (model.Candidates, error) { return model.Candidates{first, second}, nil },
		Bound:         2,
		Rounds:        2,
		TierWait:      time.Hour,
		Gate:          "echo checking; test -f ok || { echo 'FAIL: no ok' >&2; exit 1; }",
		Attempts:      3,
		HandBackLabel: "needs-decision",
		HandOffLabel:  "needs-review",
		Denylist:      []string{".github/**", "flake.lock", "**/secrets.yaml"},
		SizeSignal:    400,
		CIWait:        10 * time.Minute,
		CICeiling:     2 * time.Hour,
		CIFixes:       2,
		Store:         s,
		AskReview:     handoff.Asker(transition.Armer{Store: s, Holder: "implement-test", LeaseTTL: time.Minute}),
		StateDir:      t.TempDir(),
	}
	reg := transition.MustRegistry(implement.Transitions(d)...)
	job, err := s.Ensure(context.Background(), store.KindImplement, store.Subject{Type: store.SubjectIssue, Number: issue}, implement.Start, now)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: s, tr: tr, model: m, deps: d, remote: remote, reg: reg, job: job, at: now}
	d.Log = func(msg string) { f.logged = append(f.logged, msg) }
	f.run = &transition.Runner{Store: s, Registry: reg, Holder: "test", LeaseTTL: time.Minute, Clock: func() time.Time { return f.at }}
	return f
}

// claim runs `implement` once, from start, and then `implement-claimed` until
// the claim is read back. The outcome is the claim's own.
func (f *fixture) claim() (transition.Outcome, error) {
	f.t.Helper()
	out, err := f.run.Run(context.Background(), "implement", f.job.ID)
	if err != nil {
		return out, err
	}
	for range 3 {
		if f.now().State != implement.Claiming {
			return out, nil
		}
		if _, err := f.run.Run(context.Background(), "implement-claimed", f.job.ID); err != nil {
			return out, err
		}
	}
	f.t.Fatalf("the claim was never read back; the job is in %q", f.now().State)
	return out, nil
}

func (f *fixture) now() store.Job {
	f.t.Helper()
	job, err := f.store.Job(context.Background(), f.job.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return job
}

// setState puts the job in state, due now, the fixture state a transition is
// tested from.
func (f *fixture) setState(state string) {
	f.t.Helper()
	ctx := context.Background()
	if _, ok, err := f.store.Acquire(ctx, f.job.ID, "fixture", now, time.Minute); err != nil || !ok {
		f.t.Fatalf("Acquire = %v, %v", ok, err)
	}
	if err := f.store.Commit(ctx, store.Commit{JobID: f.job.ID, Holder: "fixture", State: state, NextRunAt: now, Release: true}); err != nil {
		f.t.Fatal(err)
	}
}

// restart puts the job back in start and due, the way intake re-arms it for a
// new command.
func (f *fixture) restart() {
	f.t.Helper()
	ctx := context.Background()
	if _, ok, err := f.store.Acquire(ctx, f.job.ID, "intake", now, time.Minute); err != nil || !ok {
		f.t.Fatalf("Acquire = %v, %v", ok, err)
	}
	if err := f.store.Commit(ctx, store.Commit{JobID: f.job.ID, Holder: "intake", State: implement.Start, NextRunAt: now, Release: true}); err != nil {
		f.t.Fatal(err)
	}
}

func command(id int64) github.Comment {
	return github.Comment{ID: id, Login: "alice", Association: "OWNER", Body: "/implement"}
}

func agentPR(number int, branch string) github.PullRequest {
	return github.PullRequest{Number: number, State: "open", HeadRef: branch, Login: agent}
}

// /implement on an open issue with no pull request from the agent is claimed,
// and the job goes on to the work, due now. Nothing is said: the answer to a
// command that starts work is the work.
func TestAnImplementCommandIsClaimedAndTheWorkStarts(t *testing.T) {
	tr := newTracker(command(1), command(2))
	f := setup(t, tr)

	if _, err := f.claim(); err != nil {
		t.Fatal(err)
	}
	if tr.claims(1) != 1 || tr.claims(2) != 1 {
		t.Errorf("claims = %d and %d, want every unanswered command claimed once", tr.claims(1), tr.claims(2))
	}
	if j := f.now(); j.State != implement.Implementing || !j.NextRunAt.Equal(now) {
		t.Errorf("job = %+v, want it due now in %s", j, implement.Implementing)
	}
	if posted := tr.byAgent(); len(posted) != 0 {
		t.Errorf("the agent said %d things, want nothing: %+v", len(posted), posted)
	}
}

// Work nobody commanded - taken through the eligibility label, or by hand - is
// the same job (ADR 0001 §14): it is claimed on the issue itself, and starts
// the same way. Nothing asked, so
// there is nothing to answer.
func TestWorkNobodyCommandedIsClaimedOnTheIssue(t *testing.T) {
	tr := newTracker()
	f := setup(t, tr)

	out, err := f.claim()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.Performed, " "); got != "claim-issue-7-0" {
		t.Errorf("performed [%s], want [claim-issue-7-0]", got)
	}
	if !intake.Claimed(tr.eyes, agent) || len(tr.eyes) != 1 {
		t.Errorf("reactions on the issue = %+v, want the agent's claim once", tr.eyes)
	}
	if j := f.now(); j.State != implement.Implementing || !j.NextRunAt.Equal(now) {
		t.Errorf("job = %+v, want it due now in %s", j, implement.Implementing)
	}
	if posted := tr.byAgent(); len(posted) != 0 {
		t.Errorf("the agent said %d things, want nothing: %+v", len(posted), posted)
	}
}

// A job that claims a command claims the issue as well: the claim on the issue
// is what intake reads to tell the issue was taken, whoever asked.
func TestCommandedWorkClaimsTheIssueToo(t *testing.T) {
	tr := newTracker(command(1))
	f := setup(t, tr)

	if _, err := f.claim(); err != nil {
		t.Fatal(err)
	}
	if !intake.Claimed(tr.eyes, agent) || len(tr.eyes) != 1 {
		t.Errorf("reactions on the issue = %+v, want the agent's claim once", tr.eyes)
	}
}

// Only a command is claimed: not a comment from an account without write
// access, not the agent's own, and not one the agent already answered.
func TestOnlyUnansweredCommandsAreClaimed(t *testing.T) {
	answered := command(3)
	tr := newTracker(
		command(1),
		github.Comment{ID: 2, Login: "mallory", Association: "NONE", Body: "/implement"},
		answered,
		github.Comment{ID: 4, Login: agent, Association: "NONE", Body: "/implement"},
		github.Comment{ID: 5, Login: "alice", Association: "OWNER", Body: "/review"},
	)
	tr.reactions[3] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f := setup(t, tr)

	out, err := f.claim()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.Performed, " "); got != "claim-comment-1-0 claim-issue-7-0" {
		t.Errorf("performed [%s], want [claim-comment-1-0 claim-issue-7-0]", got)
	}
	for _, id := range []int64{2, 4, 5} {
		if tr.claims(id) != 0 {
			t.Errorf("comment %d was claimed, and it is not a command", id)
		}
	}
}

// A closed issue has nothing to implement. The command is claimed, so intake
// stops seeing it, and nothing else happens.
func TestAClosedIssueIsClaimedAndNotImplemented(t *testing.T) {
	tr := newTracker(command(1))
	tr.state = "closed"
	f := setup(t, tr)

	if _, err := f.claim(); err != nil {
		t.Fatal(err)
	}
	if tr.claims(1) != 1 {
		t.Error("the command on a closed issue was not claimed, so intake would keep seeing it")
	}
	if posted := tr.byAgent(); len(posted) != 0 {
		t.Errorf("the agent replied on a closed issue: %+v", posted)
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest in start", j)
	}
}

// An issue that already has an open pull request from the agent gets one reply
// per command, linking it, and no second pull request.
func TestAnOpenPullRequestIsLinkedRatherThanImplementedAgain(t *testing.T) {
	tr := newTracker(command(1), command(2))
	tr.prs = []github.PullRequest{agentPR(40, "afk/7-1")}
	f := setup(t, tr)

	if _, err := f.claim(); err != nil {
		t.Fatal(err)
	}
	posted := tr.byAgent()
	if len(posted) != 2 {
		t.Fatalf("%d replies, want one per command", len(posted))
	}
	for _, c := range posted {
		if !strings.Contains(c.Body, "#40") {
			t.Errorf("reply %q does not link #40", c.Body)
		}
	}
	if tr.claims(1) != 1 || tr.claims(2) != 1 {
		t.Error("the commands were not claimed")
	}
	if j := f.now(); j.State != implement.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest in start", j)
	}

	// A third command, later: one more reply, and none repeated.
	tr.comments = append(tr.comments, command(3))
	f.restart()
	if _, err := f.claim(); err != nil {
		t.Fatal(err)
	}
	if posted := tr.byAgent(); len(posted) != 3 {
		t.Errorf("%d replies after a third command, want 3", len(posted))
	}
}

// The agent's pull request for an issue is the one it opened from a branch
// named for that issue. Another author's, or one for another issue, is not.
func TestOnlyTheAgentsPullRequestForThisIssueCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   github.PullRequest
	}{
		{"another author on the agent's branch name", github.PullRequest{Number: 40, State: "open", HeadRef: "afk/7-1", Login: "alice"}},
		{"the agent's, for another issue", agentPR(40, "afk/70-1")},
		{"the agent's, under another prefix", agentPR(40, "deps/7-1")},
		{"the agent's, not named by the agent", agentPR(40, "afk/7")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker(command(1))
			tr.prs = []github.PullRequest{tc.pr}
			f := setup(t, tr)
			if _, err := f.claim(); err != nil {
				t.Fatal(err)
			}
			if f.now().State != implement.Implementing || len(tr.byAgent()) != 0 {
				t.Errorf("job in %q with replies %+v, want the work started and nothing said", f.now().State, tr.byAgent())
			}
		})
	}

	t.Run("the agent's login, spelled differently", func(t *testing.T) {
		tr := newTracker(command(1))
		pr := agentPR(40, "afk/7-2")
		pr.Login = "AFK-Bot"
		tr.prs = []github.PullRequest{pr}
		f := setup(t, tr)
		if _, err := f.claim(); err != nil {
			t.Fatal(err)
		}
		if len(tr.byAgent()) != 1 {
			t.Errorf("the agent's own pull request was not recognised")
		}
	})
}

// A command already claimed is not claimed again when the job starts over. The
// issue's claim is asked for again, and a reaction the agent already made is
// not a second one.
func TestAClaimIsMadeOnce(t *testing.T) {
	tr := newTracker(command(1))
	f := setup(t, tr)
	if _, err := f.claim(); err != nil {
		t.Fatal(err)
	}

	// The claim is on the tracker now; a later run sees it answered.
	f.restart()
	out, err := f.claim()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.Performed, " "); strings.Contains(got, "claim-comment") {
		t.Errorf("performed [%s], want the command not claimed again", got)
	}
	if tr.claims(1) != 1 || len(tr.eyes) != 1 {
		t.Errorf("%d claims on the command and %d on the issue, want 1 of each", tr.claims(1), len(tr.eyes))
	}
}

// A hand-run that names a pull request is a mistake, and says so rather than
// implementing a pull request.
func TestAPullRequestIsNotImplemented(t *testing.T) {
	tr := newTracker(command(1))
	tr.pullRequest = true
	f := setup(t, tr)

	if _, err := f.claim(); err == nil || !strings.Contains(err.Error(), "pull request") {
		t.Errorf("err = %v, want it to say #%d is a pull request", err, issue)
	}
	if tr.claims(1) != 0 {
		t.Error("a comment on a pull request was claimed as /implement")
	}
}

func TestIssueOfReadsTheAgentsBranchNames(t *testing.T) {
	for _, tc := range []struct {
		prefix, branch string
		want           int
		ok             bool
	}{
		{"afk/", "afk/7-1", 7, true},
		{"afk/", "afk/40-12", 40, true},
		{"afk-", "afk-7-1", 7, true},
		{"afk/", "afk/7", 0, false},
		{"afk/", "afk/7-", 0, false},
		{"afk/", "afk/-1", 0, false},
		{"afk/", "afk/7-1-2", 0, false},
		{"afk/", "afk/+7-1", 0, false},
		{"afk/", "afk/0-1", 0, false},
		{"afk/", "afk/seven-1", 0, false},
		{"afk/", "other/7-1", 0, false},
		{"", "7-1", 0, false},
	} {
		got, ok := implement.IssueOf(tc.prefix, tc.branch)
		if got != tc.want || ok != tc.ok {
			t.Errorf("IssueOf(%q, %q) = %d, %v, want %d, %v", tc.prefix, tc.branch, got, ok, tc.want, tc.ok)
		}
	}
}
