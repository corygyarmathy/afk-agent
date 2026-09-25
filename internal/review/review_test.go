package review_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

const (
	agent = "afk-bot"
	head  = "0123456789abcdef0123456789abcdef01234567"
	diff  = "diff --git a/store.go b/store.go\n+func Reserve() {}\n"
)

var (
	now    = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	first  = model.Ref{Provider: "opencode-go", Model: "first"}
	second = model.Ref{Provider: "opencode-go", Model: "second"}
)

// tracker is one pull request on a fixture tracker.
type tracker struct {
	mu        sync.Mutex
	state     string
	desc      string
	author    string
	prEyes    []github.Reaction
	issues    map[int]github.Issue
	comments  []github.Comment
	reactions map[int64][]github.Reaction
	nextID    int64

	// post decides what happens to a comment the agent posts: whether it
	// lands on the pull request, and what the call reports.
	post  func(call int) (lands bool, err error)
	posts int

	// late holds a post that landed but that the tracker does not show
	// yet. The read after next sees it.
	lateFirst bool
	late      []github.Comment
}

func newTracker(comments ...github.Comment) *tracker {
	return &tracker{state: "open", comments: comments, reactions: map[int64][]github.Reaction{}, nextID: 1000}
}

func (tr *tracker) PullRequest(_ context.Context, n int) (github.PullRequest, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return github.PullRequest{Number: n, State: tr.state, HeadSHA: head, Title: "Reserve a job", Body: tr.desc, Login: tr.author}, nil
}

func (tr *tracker) Issue(_ context.Context, n int) (github.Issue, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	is, ok := tr.issues[n]
	if !ok {
		return github.Issue{}, &github.StatusError{Method: "GET", URL: fmt.Sprintf("/issues/%d", n), Code: 404, Status: "404 Not Found"}
	}
	return is, nil
}

func (tr *tracker) Diff(context.Context, int) (string, error) { return diff, nil }

func (tr *tracker) Comments(context.Context, int) ([]github.Comment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	seen := append([]github.Comment(nil), tr.comments...)
	tr.comments = append(tr.comments, tr.late...)
	tr.late = nil
	return seen, nil
}

func (tr *tracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]github.Reaction(nil), tr.reactions[id]...), nil
}

func (tr *tracker) Comment(_ context.Context, _ int, body string) (github.Comment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.posts++
	lands, err := true, error(nil)
	if tr.post != nil {
		lands, err = tr.post(tr.posts)
	}
	tr.nextID++
	c := github.Comment{ID: tr.nextID, Login: agent, Association: "COLLABORATOR", Body: body}
	if tr.lateFirst && tr.posts == 1 {
		tr.late = append(tr.late, c)
		return c, err
	}
	if lands {
		tr.comments = append(tr.comments, c)
	}
	return c, err
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

func (tr *tracker) IssueReactions(context.Context, int) ([]github.Reaction, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]github.Reaction(nil), tr.prEyes...), nil
}

func (tr *tracker) ReactToIssue(_ context.Context, _ int, content string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, r := range tr.prEyes {
		if r.Login == agent && r.Content == content {
			return nil
		}
	}
	tr.prEyes = append(tr.prEyes, github.Reaction{Login: agent, Content: content})
	return nil
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

func (tr *tracker) claimed(id int64) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return intake.Claimed(tr.reactions[id], agent)
}

// reviewer is a fixture model: each call takes the next answer.
type reviewer struct {
	answers []error
	asked   []opencode.Request
	diffs   []string
	specs   []string
}

func (m *reviewer) Run(_ context.Context, req opencode.Request) (opencode.Reply, error) {
	m.asked = append(m.asked, req)
	b, _ := os.ReadFile(filepath.Join(req.Dir, ".git", "afk-pr.diff"))
	m.diffs = append(m.diffs, string(b))
	b, _ = os.ReadFile(filepath.Join(req.Dir, ".git", "afk-pr-spec.md"))
	m.specs = append(m.specs, string(b))
	var err error
	if i := len(m.asked) - 1; i < len(m.answers) {
		err = m.answers[i]
	}
	if err != nil {
		return opencode.Reply{}, err
	}
	return opencode.Reply{Text: "The change is sound. One nit: Reserve has no test.", Cost: 0.0123}, nil
}

func transient(ref model.Ref) error {
	return &opencode.TransientError{Model: ref, Err: errors.New("429 Too Many Requests")}
}

type fixture struct {
	t     *testing.T
	store store.Store
	tr    *tracker
	model *reviewer
	deps  *review.Deps
	run   *transition.Runner
	reg   *transition.Registry
	job   store.Job
}

func setup(t *testing.T, tr *tracker, m *reviewer) *fixture {
	t.Helper()
	s := storetest.Open(t)
	d := &review.Deps{
		Tracker: tr,
		Model:   m,
		Store:   s,
		Checkout: func(_ context.Context, dir string, _ int) (string, error) {
			return head, os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
		},
		Resolve:  func(context.Context) (model.Candidates, error) { return model.Candidates{first, second}, nil },
		Bound:    2,
		TierWait: time.Hour,
		Login:    agent,
		StateDir: t.TempDir(),
	}
	reg := transition.MustRegistry(review.Transitions(d)...)
	job, err := s.Ensure(context.Background(), store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, review.Start, now)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		t: t, store: s, tr: tr, model: m, deps: d, reg: reg, job: job,
		run: &transition.Runner{Store: s, Registry: reg, Holder: "test", LeaseTTL: time.Minute, Clock: func() time.Time { return now }},
	}
}

// drive runs whatever transition the job's state calls for until the job comes
// to rest or defers, the way the pool would. It returns the errors the
// transitions returned along the way.
func (f *fixture) drive() []error {
	f.t.Helper()
	var errs []error
	for range 30 {
		job, err := f.store.Job(context.Background(), f.job.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		t, ok := f.reg.Next(job.Kind, job.State)
		if !ok {
			f.t.Fatalf("no transition runs from %q", job.State)
		}
		if _, err := f.run.Run(context.Background(), t.Name, job.ID); err != nil {
			errs = append(errs, err)
		}
		job = f.now()
		if job.NextRunAt.IsZero() || job.State == review.Deferred {
			return errs
		}
	}
	f.t.Fatalf("the job never came to rest; it is in %q", f.now().State)
	return errs
}

// restart puts the job back in start and due, the way intake re-arms it for a
// new request.
func (f *fixture) restart() {
	f.t.Helper()
	ctx := context.Background()
	if _, ok, err := f.store.Acquire(ctx, f.job.ID, "intake", now, time.Minute); err != nil || !ok {
		f.t.Fatalf("Acquire = %v, %v", ok, err)
	}
	if err := f.store.Commit(ctx, store.Commit{JobID: f.job.ID, Holder: "intake", State: review.Start, NextRunAt: now, Release: true}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) now() store.Job {
	f.t.Helper()
	job, err := f.store.Job(context.Background(), f.job.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return job
}

func command(id int64) github.Comment {
	return github.Comment{ID: id, Login: "alice", Association: "OWNER", Body: "/review"}
}

// The acceptance criterion of #3, from a fixture state: a /review produces one
// advisory review, of the head, from a model given the diff in a checkout.
func TestAReviewCommandProducesOneAdvisoryReview(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	posted := f.tr.byAgent()
	if len(posted) != 1 {
		t.Fatalf("%d comments from the agent, want 1", len(posted))
	}
	b := posted[0].Body
	for _, want := range []string{review.Marker(head), "Advisory review", "does not gate or block merging", "Reserve has no test", first.String()} {
		if !strings.Contains(b, want) {
			t.Errorf("the review does not contain %q:\n%s", want, b)
		}
	}
	if !f.tr.claimed(1) {
		t.Error("the command was not claimed")
	}
	if j := f.now(); j.State != review.Start || !j.NextRunAt.IsZero() || j.Lease != nil {
		t.Errorf("job = %+v, want it at rest in start", j)
	}

	if len(f.model.asked) != 1 {
		t.Fatalf("the model was asked %d times, want 1", len(f.model.asked))
	}
	req := f.model.asked[0]
	if req.Model != first || !strings.Contains(req.Prompt, "#12") || !strings.Contains(req.Prompt, head) {
		t.Errorf("request = %+v, want the first candidate and a prompt naming #12 at %s", req, head)
	}
	if f.model.diffs[0] != diff {
		t.Errorf("the model's workspace held diff %q, want %q", f.model.diffs[0], diff)
	}
	if _, err := os.Stat(filepath.Join(f.deps.StateDir, "workspaces", f.job.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the workspace was left behind: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.deps.StateDir, "replies")); len(entries) != 0 {
		t.Errorf("a posted reply was left in the state directory: %v", entries)
	}
}

// The review is the reviewing-changes skill's, run in a workspace it was not
// written for: a shallow checkout, and no credentials for the tracker. So the
// agent reads what the skill would have fetched - the issues the pull request
// closes - and leaves it beside the diff, verbatim.
func TestTheModelRunsTheSkillWithTheLinkedIssueAsTheSpec(t *testing.T) {
	tr := newTracker(command(1))
	tr.desc = "Reserve before running. Closes #7, fixes: #9.\n\nSee #8, and closes other/repo#5."
	tr.issues = map[int]github.Issue{
		7: {Number: 7, Title: "Jobs are reserved", Body: "A job is reserved before it runs."},
		8: {Number: 8, Title: "Unrelated", Body: "Only mentioned in passing."},
		9: {Number: 9, Title: "Reservations expire", Body: "A reservation lapses after its lease."},
	}
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	prompt := f.model.asked[0].Prompt
	for _, want := range []string{"reviewing-changes", ".git/afk-pr.diff", ".git/afk-pr-spec.md"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not name %q:\n%s", want, prompt)
		}
	}
	spec := f.model.specs[0]
	for _, want := range []string{
		"Reserve a job", tr.desc,
		"#7", "Jobs are reserved", "A job is reserved before it runs.",
		"#9", "Reservations expire", "A reservation lapses after its lease.",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("the spec does not contain %q:\n%s", want, spec)
		}
	}
	for _, unwanted := range []string{"Only mentioned in passing.", "#5"} {
		if strings.Contains(strings.ReplaceAll(spec, tr.desc, ""), unwanted) {
			t.Errorf("the spec contains %q, which the pull request does not close:\n%s", unwanted, spec)
		}
	}
	if i, j := strings.Index(spec, "Jobs are reserved"), strings.Index(spec, "Reservations expire"); i > j {
		t.Errorf("the issues are not in the order the description names them:\n%s", spec)
	}
}

// An issue the description closes that the tracker cannot find - a typo, or
// one since deleted - is a gap in the spec, not a reason to write no review.
func TestAClosedIssueThatIsNotThereIsNotedAndTheReviewGoesOn(t *testing.T) {
	tr := newTracker(command(1))
	tr.desc = "Closes #404."
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.tr.byAgent()) != 1 {
		t.Fatal("no review was posted")
	}
	if spec := f.model.specs[0]; !strings.Contains(spec, "#404") || !strings.Contains(spec, "could not be read") {
		t.Errorf("the spec does not say #404 could not be read:\n%s", spec)
	}
}

// A /review on a head already reviewed runs no model, posts no review, and says
// so - once, however often the transition is replayed.
func TestAReviewOfAHeadAlreadyReviewedSaysSo(t *testing.T) {
	earlier := github.Comment{ID: 500, Login: agent, Association: "COLLABORATOR", Body: review.Marker(head) + "\nAn earlier review."}
	tr := newTracker(command(1), earlier, command(2))
	tr.reactions[1] = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f := setup(t, tr, &reviewer{})

	for range 2 {
		if errs := f.drive(); len(errs) != 0 {
			t.Fatalf("errors: %v", errs)
		}
	}

	if len(f.model.asked) != 0 {
		t.Errorf("the model was asked %d times for a head already reviewed", len(f.model.asked))
	}
	posted := f.tr.byAgent()
	if len(posted) != 2 || !strings.Contains(posted[1].Body, "Already reviewed") {
		t.Fatalf("agent comments = %+v, want the earlier review and one reply saying so", posted)
	}
	if !f.tr.claimed(2) {
		t.Error("the new command was not claimed")
	}
}

func TestATransientFailureTriesTheNextModel(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{answers: []error{transient(first)}})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := fmt.Sprint(refs(f.model.asked)); got != fmt.Sprint([]model.Ref{first, second}) {
		t.Errorf("asked %s, want the first candidate then the second", got)
	}
	if posted := f.tr.byAgent(); len(posted) != 1 || !strings.Contains(posted[0].Body, second.String()) {
		t.Errorf("agent comments = %+v, want one review naming %s", posted, second)
	}
}

// A tier whose every candidate failed defers, and comes back to try the tier
// again from the top - not handed back (ADR 0001 §10).
func TestAnExhaustedTierDefersAndStartsOverAfterTheWait(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{answers: []error{transient(first), transient(second)}})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	j := f.now()
	if j.State != review.Deferred || !j.NextRunAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("job = %+v, want it deferred for the tier wait", j)
	}
	if len(f.tr.byAgent()) != 0 {
		t.Error("an exhausted tier posted something")
	}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := fmt.Sprint(refs(f.model.asked)); got != fmt.Sprint([]model.Ref{first, second, first}) {
		t.Errorf("asked %s, want the tier again from its first candidate", got)
	}
	if len(f.tr.byAgent()) != 1 {
		t.Errorf("%d reviews after the wait, want 1", len(f.tr.byAgent()))
	}
}

func TestALimitedBudgetDefersToTheReset(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{})
	reset := now.Add(3 * time.Hour)
	f.deps.Resolve = func(context.Context) (model.Candidates, error) { return nil, &model.LimitedError{ResetsAt: reset} }

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != review.Deferred || !j.NextRunAt.Equal(reset) {
		t.Errorf("job = %+v, want it deferred to %s", j, reset)
	}
	if len(f.model.asked) != 0 {
		t.Error("a limited budget still ran a model")
	}
}

// A capability nobody enrolled is the resolver's error, surfaced as it is and
// never a quietly downgraded run.
func TestAMissingCapabilityIsAnErrorNamingIt(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{})
	f.deps.Resolve = func(context.Context) (model.Candidates, error) {
		return nil, &model.NoCandidateError{Tier: "review", Missing: []model.Capability{model.InputModality("image")}}
	}

	errs := f.drive()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "input:image") {
		t.Fatalf("errors = %v, want one naming input:image", errs)
	}
	if len(f.model.asked) != 0 || len(f.tr.byAgent()) != 0 {
		t.Error("a run happened without a candidate")
	}
}

// Exactly one review, however the post goes wrong. A post can be lost - the
// process killed between the commit and the request, or the request dropped -
// and it can land and still report a failure. Verify reads the answer back and
// posts again only when it is not there.
func TestAReviewIsOnThePullRequestExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		post func(call int) (bool, error)
		errs int
	}{
		{"the post is lost without a word", func(call int) (bool, error) { return call > 1, nil }, 0},
		{"the post fails and is not there", func(call int) (bool, error) {
			if call == 1 {
				return false, errors.New("502 Bad Gateway")
			}
			return true, nil
		}, 1},
		{"the post lands and reports a failure", func(call int) (bool, error) {
			if call == 1 {
				return true, errors.New("read: connection reset by peer")
			}
			return true, nil
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker(command(1))
			tr.post = tc.post
			f := setup(t, tr, &reviewer{})

			if errs := f.drive(); len(errs) != tc.errs {
				t.Fatalf("errors = %v, want %d", errs, tc.errs)
			}
			if posted := f.tr.byAgent(); len(posted) != 1 {
				t.Fatalf("%d reviews on the pull request, want exactly 1", len(posted))
			}
			if len(f.model.asked) != 1 {
				t.Errorf("the model was asked %d times; a re-post must not pay for another run", len(f.model.asked))
			}
			if j := f.now(); j.State != review.Start || !j.NextRunAt.IsZero() {
				t.Errorf("job = %+v, want it at rest", j)
			}
		})
	}
}

// A reply that never appears is not posted forever. After the bound, the job
// hands back with an error for the operator.
func TestPostingRoundsAreBounded(t *testing.T) {
	tr := newTracker(command(1))
	tr.post = func(int) (bool, error) { return false, nil }
	f := setup(t, tr, &reviewer{})

	errs := f.drive()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "never appeared") {
		t.Fatalf("errors = %v, want one saying the review never appeared", errs)
	}
	if tr.posts != f.deps.Bound {
		t.Errorf("posted %d times, want the bound, %d", tr.posts, f.deps.Bound)
	}
	if j := f.now(); !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it parked", j)
	}
}

func TestAClosedPullRequestIsClaimedAndNotReviewed(t *testing.T) {
	tr := newTracker(command(1))
	tr.state = "closed"
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(f.model.asked) != 0 || len(tr.byAgent()) != 0 {
		t.Error("a closed pull request was reviewed")
	}
	if !tr.claimed(1) {
		t.Error("the command on a closed pull request was not claimed, so intake would keep seeing it")
	}
}

// Git checks the pull request's head out from a repository on disk. Offline:
// the remote is a directory.
func TestGitChecksOutThePullRequestHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	src := t.TempDir()
	gitIn(t, src, "init", "--quiet")
	os.WriteFile(filepath.Join(src, "store.go"), []byte("package store\n"), 0o644)
	gitIn(t, src, "add", ".")
	gitIn(t, src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "head")
	want := gitIn(t, src, "rev-parse", "HEAD")
	gitIn(t, src, "update-ref", "refs/pull/7/head", "HEAD")

	dst := t.TempDir()
	got, err := review.Git{Remote: src}.Checkout(context.Background(), dst, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("checked out %s, want %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dst, "store.go")); err != nil {
		t.Errorf("the head's files are not in the workspace: %v", err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func refs(reqs []opencode.Request) []model.Ref {
	var out []model.Ref
	for _, r := range reqs {
		out = append(out, r.Model)
	}
	return out
}

// A post that landed too late for verify to see is not posted a second time:
// the next round reads the tracker again before it posts, and finds it.
func TestAPostThatLandsLateIsNotPostedAgain(t *testing.T) {
	tr := newTracker(command(1))
	tr.lateFirst = true
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if posted := tr.byAgent(); len(posted) != 1 {
		t.Fatalf("%d reviews on the pull request, want exactly 1", len(posted))
	}
	if tr.posts != 1 {
		t.Errorf("posted %d times, want 1: the second round should have found the first", tr.posts)
	}
}

// The implement job asks for a review by making the job due, with no command:
// its pull request is the request, claimed with a 👀 on the description, and
// the review says which job asked.
func TestTheImplementJobsPullRequestIsARequest(t *testing.T) {
	tr := newTracker()
	tr.author = agent
	tr.desc = "<!-- afk:implement issue=7 -->\nCloses #7."
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if !intake.Claimed(tr.prEyes, agent) {
		t.Error("the pull request was not claimed")
	}
	posted := tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "Asked for by the implement job for #7") {
		t.Fatalf("agent comments = %+v, want one review saying the implement job asked", posted)
	}

	// Claimed once: a later run of the job - a /review, say - does not
	// claim the description again.
	tr.comments = append(tr.comments, command(9))
	f.restart()
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if n := len(tr.prEyes); n != 1 {
		t.Errorf("%d reactions on the pull request, want 1", n)
	}
}

// Only the agent's own pull request can ask: a human's that carries the
// marker is not a request, and the job reviews it only if a command asks.
func TestAMarkerFromAnyoneElseIsNotARequest(t *testing.T) {
	tr := newTracker()
	tr.author = "mallory"
	tr.desc = "<!-- afk:implement issue=7 -->"
	f := setup(t, tr, &reviewer{})
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(tr.prEyes) != 0 {
		t.Error("a human's pull request was claimed as the implement job's request")
	}
	if posted := tr.byAgent(); len(posted) == 1 && strings.Contains(posted[0].Body, "implement job") {
		t.Error("the review says the implement job asked for it")
	}
}
