package review_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/spend"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

const (
	agent = "afk-bot"
	head  = "0123456789abcdef0123456789abcdef01234567"
	diff  = "diff --git a/store.go b/store.go\n+func Reserve() {}\n"
	delta = "diff --git a/store.go b/store.go\n+func Release() {}\n"
	read  = "fedcba9876543210fedcba9876543210fedcba98"
	base  = "89abcdef0123456789abcdef0123456789abcdef"
)

var (
	now    = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	first  = model.Ref{Provider: "opencode-go", Model: "first"}
	second = model.Ref{Provider: "opencode-go", Model: "second"}
)

// tracker is one pull request on a fixture tracker.
type tracker struct {
	mu     sync.Mutex
	state  string
	desc   string
	author string
	prEyes []github.Reaction

	// losePREyes is how many of the agent's next reactions on the pull
	// request are reported made and never land: what a kill between the
	// commit and the reaction leaves.
	losePREyes int
	issues     map[int]github.Issue
	comments   []github.Comment
	reactions  map[int64][]github.Reaction
	nextID     int64

	// post decides what happens to a comment the agent posts: whether it
	// lands on the pull request, and what the call reports.
	post  func(call int) (lands bool, err error)
	posts int

	// late holds a post that landed but that the tracker does not show
	// yet. The read after next sees it.
	lateFirst bool
	late      []github.Comment

	// diffErr decides what the diff read returns, by call.
	diffErr func(call int) error

	// compares is every diff between two commits read, as base...head.
	compares []string

	// reviews is the submitted pull request reviews, and lines their line
	// comments by review.
	reviews []github.PullRequestReview
	lines   map[int64][]github.LineComment
}

func newTracker(comments ...github.Comment) *tracker {
	return &tracker{state: "open", comments: comments, reactions: map[int64][]github.Reaction{}, nextID: 1000}
}

func (tr *tracker) PullRequest(_ context.Context, n int) (github.PullRequest, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return github.PullRequest{Number: n, State: tr.state, HeadSHA: head, BaseSHA: base, Title: "Reserve a job", Body: tr.desc, Login: tr.author}, nil
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

// Compare is the pull request's diff from its base, and the delta from
// anywhere else.
func (tr *tracker) Compare(_ context.Context, from, to string) (string, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.compares = append(tr.compares, from+"..."+to)
	if tr.diffErr != nil {
		if err := tr.diffErr(len(tr.compares)); err != nil {
			return "", err
		}
	}
	if from == base {
		return diff, nil
	}
	return delta, nil
}

func (tr *tracker) PullRequestReviews(context.Context, int) ([]github.PullRequestReview, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.reviews, nil
}

func (tr *tracker) LineComments(_ context.Context, _ int, review int64) ([]github.LineComment, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.lines[review], nil
}

func badGateway() error {
	return &github.StatusError{Method: "GET", URL: "/pulls/12", Code: 502, Status: "502 Bad Gateway"}
}

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
	if tr.losePREyes > 0 {
		tr.losePREyes--
		return nil
	}
	for _, r := range tr.prEyes {
		if r.Login == agent && r.Content == content {
			return nil
		}
	}
	tr.prEyes = append(tr.prEyes, github.Reaction{Login: agent, Content: content})
	return nil
}

// Label applies a label to the pull request, which the tracker reads as an
// issue.
func (tr *tracker) Unlabel(context.Context, int, string) error {
	return errors.New("the review kind never takes a label off")
}

func (tr *tracker) Label(_ context.Context, n int, label string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.issues == nil {
		tr.issues = map[int]github.Issue{}
	}
	is := tr.issues[n]
	is.Number = n
	is.Labels = append(is.Labels, label)
	tr.issues[n] = is
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
	// replies, by call, replace the fixture's reply text.
	replies []string
	// subAgents is how many sub-agents' sessions every reply's run started,
	// and unread how many of those it could not read.
	subAgents, unread int
	// failed is what a run that fails spent, and silent a run that
	// succeeds and reports no spend at all.
	failed opencode.Reply
	silent bool
	asked  []opencode.Request
	diffs  []string
	wholes []string
	specs  []string
}

func (m *reviewer) Run(_ context.Context, req opencode.Request) (opencode.Reply, error) {
	m.asked = append(m.asked, req)
	b, _ := os.ReadFile(filepath.Join(req.Dir, ".git", "afk-pr.diff"))
	m.diffs = append(m.diffs, string(b))
	b, _ = os.ReadFile(filepath.Join(req.Dir, ".git", "afk-pr-whole.diff"))
	m.wholes = append(m.wholes, string(b))
	b, _ = os.ReadFile(filepath.Join(req.Dir, ".git", "afk-pr-spec.md"))
	m.specs = append(m.specs, string(b))
	var err error
	if i := len(m.asked) - 1; i < len(m.answers) {
		err = m.answers[i]
	}
	if err != nil {
		return m.failed, err
	}
	text := "The change is sound. One nit: Reserve has no test."
	if i := len(m.asked) - 1; i < len(m.replies) {
		text = m.replies[i]
	}
	if m.silent {
		return opencode.Reply{Text: text}, nil
	}
	return opencode.Reply{Text: text, Cost: 0.0123, Tokens: opencode.Tokens{Input: 2000, Output: 300}, SubAgents: m.subAgents, Unread: m.unread}, nil
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

	// last and lastErr are what drive's last run returned: an error with
	// Parked is the outcome dispatch tells the operator about.
	last    transition.Outcome
	lastErr error
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
		Resolve:       func(context.Context) (model.Candidates, error) { return model.Candidates{first, second}, nil },
		Bound:         2,
		Rounds:        2,
		HandBackLabel: "needs-decision",
		TierWait:      time.Hour,
		Repo:          "owner/name",
		Login:         agent,
		StateDir:      t.TempDir(),
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
		out, err := f.run.Run(context.Background(), t.Name, job.ID)
		if err != nil {
			errs = append(errs, err)
		}
		f.last, f.lastErr = out, err
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
	for _, want := range []string{"reviewing-changes", ".git/afk-pr.diff", ".git/afk-pr-spec.md", "No one is in the session"} {
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

// The first piece of an issue too big for one pull request does not close it,
// and the issue is still what the piece is reviewed against, headed as only
// partly done by it (#127). The issue filed for the rest is there too, headed
// as out of scope, so that what the piece leaves for it is not read as
// missing.
func TestAPartOfPullRequestIsReviewedAgainstItsIssue(t *testing.T) {
	tr := newTracker(command(1))
	tr.desc = "<!-- afk:implement issue=7 -->\nPart of #7. The rest is #9.\n"
	tr.issues = map[int]github.Issue{
		7: {Number: 7, Title: "Jobs are reserved", Body: "A job is reserved before it runs."},
		9: {Number: 9, Title: "The rest of #7", Body: "Expiry is left."},
	}
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	spec := strings.ReplaceAll(f.model.specs[0], tr.desc, "")
	for _, want := range []string{
		"# Issue #7: Jobs are reserved (only partly done", "A job is reserved before it runs.",
		"# Issue #9: The rest of #7 (out of scope", "Expiry is left.",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("the spec does not say %q:\n%s", want, spec)
		}
	}
	if strings.Count(spec, "A job is reserved before it runs.") != 1 {
		t.Errorf("the spec carries #7 more than once:\n%s", spec)
	}
	if !strings.Contains(f.model.asked[0].Prompt, "out of scope") {
		t.Errorf("the prompt does not say what an issue out of scope is:\n%s", f.model.asked[0].Prompt)
	}
}

// "Part of" is read only where the implement kind writes it, straight after
// its marker: in anyone's prose, it names an issue the pull request is not
// reviewed against.
func TestPartOfInProseIsNotALink(t *testing.T) {
	tr := newTracker(command(1))
	tr.desc = "Closes #7\n\nThis is part of #8, the map of the work.\n"
	tr.issues = map[int]github.Issue{
		7: {Number: 7, Title: "Jobs are reserved", Body: "A job is reserved before it runs."},
		8: {Number: 8, Title: "The map", Body: "Everything, eventually."},
	}
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	spec := strings.ReplaceAll(f.model.specs[0], tr.desc, "")
	if !strings.Contains(spec, "A job is reserved before it runs.") || strings.Contains(spec, "Everything, eventually.") {
		t.Errorf("the spec is not #7 alone:\n%s", spec)
	}
}

func TestPartOfReadsTheImplementLinkLine(t *testing.T) {
	for desc, want := range map[string][3]int{
		"<!-- afk:implement issue=7 -->\nPart of #7. The rest is #9.\n\nmore": {7, 9, 1},
		"<!-- afk:implement issue=7 -->\r\nPart of #7. The rest is #9.\r\n":   {7, 9, 1},
		"<!-- afk:implement issue=7 -->\nPart of #7. The rest is #9.":         {7, 9, 1},
		"<!-- afk:implement issue=7 -->\nCloses #7\n":                         {0, 0, 0},
		"Part of #7. The rest is #9.\n":                                       {0, 0, 0},
		"text\n<!-- afk:implement issue=7 -->\nPart of #7. The rest is #9.\n": {0, 0, 0},
	} {
		issue, rest, ok := review.PartOf(desc)
		if got := [3]int{issue, rest, map[bool]int{true: 1}[ok]}; got != want {
			t.Errorf("PartOf(%q) = %v, want %v", desc, got, want)
		}
	}
}

// The advisory review is unaware of the sensitive line and of what the work
// cost: the spec it reads is the description without either, and otherwise
// as it was.
func TestTheSpecLeavesOutTheSensitiveLineAndTheSpend(t *testing.T) {
	tr := newTracker(command(1))
	top := "<!-- afk:implement issue=7 -->\nCloses #7\n\n"
	rest := "> **Your review** (x): read #7 first.\n\n## Start here\n\nok:1\n"
	var spent spend.Spent
	spent.Add(context.Background(), first, nil, opencode.Reply{Cost: 0.5, Tokens: opencode.Tokens{Input: 1}})
	tr.desc = top + "**Sensitive:** job store schema (`store/schema.sql`)\n\n" + rest + "\n" + spent.Footer() + "\n"
	tr.author = agent
	tr.issues = map[int]github.Issue{7: {Number: 7, Title: "Jobs are reserved", Body: "A job is reserved before it runs."}}
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	spec := f.model.specs[0]
	if strings.Contains(spec, "Sensitive") || strings.Contains(spec, "store/schema.sql") {
		t.Errorf("the spec carries the sensitive line:\n%s", spec)
	}
	if strings.Contains(spec, "afk:spend") || strings.Contains(spec, "$0.5000") {
		t.Errorf("the spec carries the spend footer:\n%s", spec)
	}
	if !strings.Contains(spec, top+rest) {
		t.Errorf("the spec does not carry the rest of the description as it was:\n%s", spec)
	}
}

// A description a human wrote is theirs, and is reviewed as they wrote it:
// one that quotes the agent's sensitive line and spend footer, as a pull
// request about them would, keeps both.
func TestTheSpecKeepsAHumansDescriptionWhole(t *testing.T) {
	tr := newTracker(command(1))
	var spent spend.Spent
	spent.Add(context.Background(), first, nil, opencode.Reply{Cost: 0.5, Tokens: opencode.Tokens{Input: 1}})
	tr.desc = "<!-- afk:implement issue=7 -->\nCloses #7\n\n**Sensitive:** job store schema (`store/schema.sql`)\n\n> **Your review** (x): read #7 first.\n\nThe footer looks like this:\n\n" + spent.Footer() + "\n\nand that is all.\n"
	tr.author = "alice"
	tr.issues = map[int]github.Issue{7: {Number: 7, Title: "Jobs are reserved", Body: "A job is reserved before it runs."}}
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if spec := f.model.specs[0]; !strings.Contains(spec, tr.desc) {
		t.Errorf("the spec does not carry the human's description as they wrote it:\n%s", spec)
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
	if f.last.Exhausted == nil {
		t.Errorf("last run = %+v; want it to say the tier is exhausted, which dispatch tells the operator about (#76)", f.last)
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

// An exhausted tier carries what the last candidate's run failed with, which
// is what the operator is told, and every candidate's failure is logged as it
// happens: nothing else keeps it once the next candidate runs (#98).
func TestAnExhaustedTierSaysWhatTheLastRunFailedWith(t *testing.T) {
	killed := &opencode.TransientError{Model: second, Err: errors.New("the run was still going after 30m0s, and was killed"), Bound: 30 * time.Minute}
	f := setup(t, newTracker(command(1)), &reviewer{answers: []error{transient(first), killed}})
	var logged []string
	f.deps.Log = func(msg string) { logged = append(logged, msg) }

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != review.Deferred {
		t.Fatalf("job = %+v, want it deferred", j)
	}
	var last *opencode.TransientError
	if !errors.As(f.last.Exhausted, &last) || last != killed {
		t.Errorf("the exhausted tier says %v; want it to carry the last run's failure, %v", f.last.Exhausted, killed)
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "429 Too Many Requests") || !strings.Contains(logged[1], "was killed") {
		t.Errorf("logged %q, want one line for each candidate's failure", logged)
	}
}

// A tracker error before the model runs is not the model's, and does not move
// the review on to the next candidate (#62).
func TestAnErrorBeforeTheModelRunsKeepsTheCandidate(t *testing.T) {
	tr := newTracker(command(1))
	tr.diffErr = func(call int) error {
		if call == 1 {
			return badGateway()
		}
		return nil
	}
	f := setup(t, tr, &reviewer{})
	f.run.Backoff = func(int) (time.Time, bool) { return now, true }

	if errs := f.drive(); len(errs) != 1 {
		t.Fatalf("errors: %v, want the one 502", errs)
	}
	if got := fmt.Sprint(refs(f.model.asked)); got != fmt.Sprint([]model.Ref{first}) {
		t.Errorf("asked %s, want the first candidate, once", got)
	}
	if posted := f.tr.byAgent(); len(posted) != 1 || !strings.Contains(posted[0].Body, first.String()) {
		t.Errorf("agent comments = %+v, want one review naming %s", posted, first)
	}
}

// An error that keeps coming back parks the review where it is, as any other
// transition's does, rather than spend the tier and defer without end (#62).
func TestAnErrorThatRecursParksTheReview(t *testing.T) {
	tr := newTracker(command(1))
	tr.diffErr = func(int) error { return badGateway() }
	f := setup(t, tr, &reviewer{})
	f.run.Backoff = func(attempts int) (time.Time, bool) { return now, attempts < 3 }

	if errs := f.drive(); len(errs) != 3 {
		t.Fatalf("errors: %v, want three", errs)
	}
	j := f.now()
	if j.State != review.Reviewing || !j.NextRunAt.IsZero() || j.Attempts != 3 {
		t.Errorf("job = %+v, want it parked in %s after 3 attempts", j, review.Reviewing)
	}
	if len(f.model.asked) != 0 {
		t.Errorf("asked %s, want no model run", refs(f.model.asked))
	}
	if f.lastErr == nil || !f.last.Parked {
		t.Errorf("last run = %+v, %v; want a failure that parked, which dispatch tells the operator about", f.last, f.lastErr)
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
	if f.last.Exhausted != nil {
		t.Errorf("last run says the tier is exhausted (%v); a limited budget is told at admission, not as a tier", f.last.Exhausted)
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

// A reply that never appears is not posted forever. Out of rounds, the review
// is handed back on the pull request, saying why, and the job rests. A review
// asked for again has rounds of its own.
func TestAReviewOutOfRoundsIsHandedBack(t *testing.T) {
	tr := newTracker(command(1))
	rounds := 2
	tr.post = func(call int) (bool, error) {
		if call <= rounds {
			return false, errors.New("POST comment: 422 Unprocessable Entity: body is too long")
		}
		return true, nil
	}
	f := setup(t, tr, &reviewer{})
	f.deps.Rounds = rounds

	errs := f.drive()
	if len(errs) != rounds {
		t.Fatalf("errors = %v, want the %d failed posts", errs, rounds)
	}
	if tr.posts != rounds+1 {
		t.Errorf("posted %d times, want the %d rounds and the hand-back", tr.posts, rounds)
	}
	posted := tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, review.HandBackMarker(head)) ||
		!strings.Contains(posted[0].Body, "never appeared") || !strings.Contains(posted[0].Body, "body is too long") {
		t.Fatalf("the agent said %+v, want one hand-back quoting the last error", posted)
	}
	if review.Reviewed(posted, agent, head) {
		t.Error("the hand-back reads as a review of the head")
	}
	if is := tr.issues[12]; strings.Join(is.Labels, ",") != "needs-decision" {
		t.Errorf("labels %v, want the hand-back label once", is.Labels)
	}
	if j := f.now(); j.State != review.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}

	// Asked again, for the same head: the posts that ran out are spent, and
	// this review is posted under rounds of its own.
	tr.comments = append(tr.comments, command(2))
	f.restart()
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	if !review.Reviewed(tr.byAgent(), agent, head) {
		t.Error("the review asked for again was not posted")
	}
	if j := f.now(); j.State != review.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// A hand-back whose commit is lost is decided again from the reply it kept:
// no second model run, and no fresh rounds of posts.
func TestAReviewHandBackWhoseCommitIsLostIsMadeAgain(t *testing.T) {
	tr := newTracker(command(1))
	rounds := 2
	tr.post = func(call int) (bool, error) {
		if call <= rounds {
			return false, errors.New("POST comment: 422 Unprocessable Entity: body is too long")
		}
		return true, nil
	}
	m := &reviewer{}
	f := setup(t, tr, m)
	f.deps.Rounds = rounds
	f.run.Store = &losesCommit{Store: f.store, state: review.HandingBack}

	f.drive()
	if len(m.asked) != 1 {
		t.Errorf("the model ran %d times, want once", len(m.asked))
	}
	if tr.posts != rounds+1 {
		t.Errorf("posted %d times, want the %d rounds and the hand-back", tr.posts, rounds)
	}
	if posted := tr.byAgent(); len(posted) != 1 || !strings.Contains(posted[0].Body, review.HandBackMarker(head)) {
		t.Fatalf("the agent said %+v, want one hand-back", posted)
	}
	if j := f.now(); j.State != review.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// losesCommit is a store that loses the first commit into state, the way a
// kill or a lost lease would.
type losesCommit struct {
	store.Store
	state string
	lost  bool
}

func (s *losesCommit) Commit(ctx context.Context, c store.Commit) error {
	if !s.lost && c.State == s.state {
		s.lost = true
		return store.ErrNotHeld
	}
	return s.Store.Commit(ctx, c)
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
	got, err := review.Git{Remote: git.Remote{URL: src}, Relays: t.TempDir()}.Checkout(context.Background(), dst, 7)
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

// As the command surface builds it, the remote refuses to be reached from a
// workspace, and a review's checkout is in one: the fetch that carries the
// token is made in a relay outside it, and the head brought in from there.
func TestGitChecksOutIntoAWorkspaceTheRemoteRefuses(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	src := t.TempDir()
	gitIn(t, src, "init", "--quiet")
	gitIn(t, src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "one")
	gitIn(t, src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "head")
	want := gitIn(t, src, "rev-parse", "HEAD")
	gitIn(t, src, "update-ref", "refs/pull/7/head", "HEAD")

	state := t.TempDir()
	workspaces, relays := filepath.Join(state, "workspaces"), filepath.Join(state, "relays")
	dst := filepath.Join(workspaces, "review-7")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	remote := git.Remote{URL: src, Untrusted: []string{workspaces}}
	got, err := review.Git{Remote: remote, Relays: relays}.Checkout(context.Background(), dst, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("checked out %s, want %s", got, want)
	}
	if n := gitIn(t, dst, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("the checkout has %s commits, want the one head: a review's fetch is shallow", n)
	}
	if entries, _ := os.ReadDir(relays); len(entries) != 0 {
		t.Errorf("the relay was left behind: %v", entries)
	}
}

// The fetch carries the token, and nothing of it is left in the workspace the
// model reads, or in the agent user's configuration, which the session could
// write. Nor does that configuration redirect the fetch, or run a hook when
// the head is checked out (#65).
func TestGitLeavesTheTokenNowhereTheSessionCanRead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	src := t.TempDir()
	gitIn(t, src, "init", "--quiet")
	gitIn(t, src, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "head")
	want := gitIn(t, src, "rev-parse", "HEAD")
	gitIn(t, src, "update-ref", "refs/pull/7/head", "HEAD")

	elsewhere := t.TempDir()
	gitIn(t, elsewhere, "init", "--quiet")
	gitIn(t, elsewhere, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "elsewhere")
	gitIn(t, elsewhere, "update-ref", "refs/pull/7/head", "HEAD")
	hooks, ran := t.TempDir(), filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[url \""+elsewhere+"\"]\n\tinsteadOf = "+src+"\n[core]\n\thooksPath = "+hooks+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	minted := 0
	remote := git.Remote{URL: src, Token: func(context.Context) (string, error) {
		minted++
		return "ghs_secret", nil
	}}
	dst := t.TempDir()
	got, err := review.Git{Remote: remote, Relays: t.TempDir()}.Checkout(context.Background(), dst, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("checked out %s, want %s from the remote rather than its redirect", got, want)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the checkout ran a hook from the agent user's configuration")
	}
	if minted == 0 {
		t.Fatal("no token was minted, so none could have been left")
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_secret"))
	for _, root := range []string{dst, global} {
		filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() {
				return err
			}
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte("ghs_secret")) || bytes.Contains(b, []byte(basic)) {
				t.Errorf("the token is in %s", path)
			}
			return nil
		})
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

// The claim on the implement job's request is read back, like a command's: a
// 👀 on the description lost between the commit and the reaction is made
// again (#58). Its key lasts the life of the pull request, so nothing else
// would ever make it.
func TestALostClaimOnTheImplementJobsPullRequestIsMadeAgain(t *testing.T) {
	tr := newTracker()
	tr.author = agent
	tr.desc = "<!-- afk:implement issue=7 -->\nCloses #7."
	tr.losePREyes = 1
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if n := len(tr.prEyes); n != 1 || !intake.Claimed(tr.prEyes, agent) {
		t.Errorf("reactions on the pull request = %v, want the one claim", tr.prEyes)
	}
	posted := tr.byAgent()
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "Asked for by the implement job for #7") {
		t.Fatalf("agent comments = %+v, want one review saying the implement job asked", posted)
	}
}

// An "already reviewed" reply that fails is made again, once (#58).
func TestAFailedAlreadyReviewedReplyIsMadeAgain(t *testing.T) {
	earlier := github.Comment{ID: 500, Login: agent, Association: "COLLABORATOR", Body: review.Marker(head) + "\nAn earlier review."}
	tr := newTracker(earlier, command(2))
	tr.post = func(call int) (bool, error) {
		if call == 1 {
			return false, errors.New("POST comment: 502 Bad Gateway")
		}
		return true, nil
	}
	f := setup(t, tr, &reviewer{})

	errs := f.drive()
	if len(errs) != 1 {
		t.Errorf("errors: %v, want the one failed reply", errs)
	}
	posted := f.tr.byAgent()
	if len(posted) != 2 || !strings.Contains(posted[1].Body, "Already reviewed") {
		t.Fatalf("agent comments = %+v, want the earlier review and one reply saying so", posted)
	}
	if j := f.now(); j.State != review.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// What asked is decided when the request is claimed, not read off who wrote
// the pull request: once the implement job's request is claimed, a /review on
// the same pull request is a human's, and its review does not say the job
// asked.
func TestAReviewAHumanAskedForOnTheImplementJobsPullRequestSaysSo(t *testing.T) {
	tr := newTracker(command(1))
	tr.author = agent
	tr.desc = "<!-- afk:implement issue=7 -->\nCloses #7."
	tr.prEyes = []github.Reaction{{Login: agent, Content: intake.Claim}}
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	posted := tr.byAgent()
	if len(posted) != 1 {
		t.Fatalf("agent comments = %+v, want one review", posted)
	}
	if strings.Contains(posted[0].Body, "implement job") {
		t.Errorf("a review a human asked for says the implement job asked:\n%s", posted[0].Body)
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

// The review is posted so that reading it after the operator's own reading is
// the easy path (#124): one comment, all of it inside one <details>, whose
// summary names the head and nothing else - no counts, no verdict.
func TestAReviewIsPostedCollapsedUnderASummaryNamingOnlyTheHead(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{})
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	posted := f.tr.byAgent()
	if len(posted) != 1 {
		t.Fatalf("%d comments from the agent, want 1", len(posted))
	}
	b := strings.TrimSpace(posted[0].Body)
	if !strings.HasPrefix(b, "<details>") || !strings.HasSuffix(b, "</details>") || strings.Count(b, "<details>") != 1 {
		t.Fatalf("the review is not wholly inside one <details>:\n%s", b)
	}
	summary := "<summary>Advisory review of <code>" + git.Short(head) + "</code>. Open it after your own reading.</summary>"
	if !strings.HasPrefix(b, "<details>\n"+summary+"\n") {
		t.Errorf("the review's summary is not %q:\n%s", summary, b)
	}
	for _, want := range []string{review.Marker(head), "does not gate or block merging", "Reserve has no test"} {
		if !strings.Contains(b, want) {
			t.Errorf("the review does not contain %q:\n%s", want, b)
		}
	}
}

// The review asks for its sub-agents' cost, and says what the run cost: as
// the model's alone when it started none, as the total of it and its
// sub-agents when it did, since a sub-agent need not run on the same model,
// and as a floor rather than the whole when some of theirs could not be read
// (#99).
func TestAReviewSaysWhatItCostAndWhetherThatIsAllOfIt(t *testing.T) {
	for _, tc := range []struct {
		subAgents, unread int
		want, not         string
	}{
		{0, 0, "<br>\nopencode-go/first · 2k in · 300 out · $0.0123</sub>", "≥"},
		{2, 0, "<br>\nopencode-go/first and its sub-agents · 2k in · 300 out · $0.0123</sub>", "≥"},
		{2, 1, "<br>\nopencode-go/first and its sub-agents · 2k in · 300 out · ≥ $0.0123, with 1 sub-agent's cost unread</sub>", ""},
		{2, 2, "<br>\nopencode-go/first and its sub-agents · 2k in · 300 out · ≥ $0.0123, with 2 sub-agents' cost unread</sub>", ""},
	} {
		m := &reviewer{subAgents: tc.subAgents, unread: tc.unread}
		f := setup(t, newTracker(command(1)), m)
		if errs := f.drive(); len(errs) != 0 {
			t.Fatalf("errors: %v", errs)
		}
		if len(m.asked) == 0 || !m.asked[0].Cost {
			t.Errorf("the review did not ask for its sub-agents' cost: %+v", m.asked)
		}
		b := f.tr.byAgent()[0].Body
		if !strings.Contains(b, tc.want) {
			t.Errorf("with %d sub-agents and %d unread, the review does not contain %q:\n%s", tc.subAgents, tc.unread, tc.want, b)
		}
		if tc.not != "" && strings.Contains(b, tc.not) {
			t.Errorf("with %d sub-agents and %d unread, the review contains %q:\n%s", tc.subAgents, tc.unread, tc.not, b)
		}
	}
}

// A run that failed on one model was paid for, and the review written on the
// next says so: a line for each model, each named as enrolled (#22). What
// was spent changes nothing about which candidate ran next.
func TestAReviewCountsTheRunsThatFailedBeforeIt(t *testing.T) {
	m := &reviewer{answers: []error{transient(first)}, failed: opencode.Reply{Cost: 0.002, Tokens: opencode.Tokens{Input: 900, Output: 10}}}
	f := setup(t, newTracker(command(1)), m)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := fmt.Sprint(refs(m.asked)); got != fmt.Sprint([]model.Ref{first, second}) {
		t.Errorf("asked %s, want the first candidate then the second", got)
	}
	b := f.tr.byAgent()[0].Body
	for _, want := range []string{
		"<br>\nopencode-go/first · 900 in · 10 out · $0.0020<br>",
		"<br>\nopencode-go/second · 2k in · 300 out · $0.0123</sub>",
		"not the account's spend",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the review does not contain %q:\n%s", want, b)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(f.deps.StateDir, "spent")); len(entries) != 0 {
		t.Errorf("the review at rest kept what it spent: %v", entries)
	}
}

// Each review counts its spend from its claim. Runs that failed for a review
// that was never written - here the job deferred, then went back to claim -
// are not in the footer of the next one.
func TestAReviewDoesNotCountAnEarlierReviewsRuns(t *testing.T) {
	m := &reviewer{answers: []error{transient(first), transient(second)}, failed: opencode.Reply{Cost: 0.002, Tokens: opencode.Tokens{Input: 900, Output: 10}}}
	f := setup(t, newTracker(command(1)), m)
	f.drive()
	if j := f.now(); j.State != review.Deferred {
		t.Fatalf("job in %q, want %q", j.State, review.Deferred)
	}

	f.restart()
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	b := f.tr.byAgent()[0].Body
	if want := "<br>\nopencode-go/first · 2k in · 300 out · $0.0123</sub>"; !strings.Contains(b, want) {
		t.Errorf("the review does not say %q:\n%s", want, b)
	}
	if strings.Contains(b, "900") || strings.Contains(b, "opencode-go/second") {
		t.Errorf("the review counts the runs of one that was never written:\n%s", b)
	}
}

// What a run spent decides nothing (ADR 0001 §11): a tier run out by
// transient failures stays, moves to the next candidate and defers exactly as
// it does when no run reports any spend.
func TestWhatTheReviewSpentDecidesNothing(t *testing.T) {
	trace := func(failed opencode.Reply) []string {
		m := &reviewer{answers: []error{transient(first), transient(second)}, failed: failed}
		f := setup(t, newTracker(command(1)), m)
		var steps []string
		for range 30 {
			job := f.now()
			next, ok := f.reg.Next(job.Kind, job.State)
			if !ok || job.NextRunAt.IsZero() || job.State == review.Deferred {
				break
			}
			out, err := f.run.Run(context.Background(), next.Name, job.ID)
			job = f.now()
			steps = append(steps, fmt.Sprintf("%s -> %s stays=%d attempts=%d at=%s exhausted=%v err=%v", next.Name, job.State, job.Stays, job.Attempts, job.NextRunAt, out.Exhausted, err))
		}
		return steps
	}
	without, with := trace(opencode.Reply{}), trace(opencode.Reply{Cost: 0.002, Tokens: opencode.Tokens{Input: 900, Output: 10}})
	if strings.Join(with, "\n") != strings.Join(without, "\n") {
		t.Errorf("with spend reported, the review went\n%s\nand without\n%s", strings.Join(with, "\n"), strings.Join(without, "\n"))
	}
	if !strings.Contains(strings.Join(without, "\n"), "-> "+review.Deferred) {
		t.Errorf("the review never deferred:\n%s", strings.Join(without, "\n"))
	}
}

// A review whose runs reported no spend has no footer, rather than a zero
// one: missing data reads as missing.
func TestAReviewWithNoSpendReportedHasNoFooter(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{silent: true})
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if b := f.tr.byAgent()[0].Body; strings.Contains(b, "<sub>") || strings.Contains(b, "$") {
		t.Errorf("a review with no spend reported has a footer:\n%s", b)
	}
}

// The prompt carries the parameters the skill takes, and asks for citations in
// the form the agent links.
func TestThePromptCarriesTheFloorAndTheFoldCut(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{})
	f.deps.Floor = "blocker"
	f.deps.FoldCut = 120
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	prompt := f.model.asked[0].Prompt
	for _, want := range []string{
		"severity floor of `blocker`",
		"fold cut of 120",
		"`path:line`",
		"<details>",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not contain %q:\n%s", want, prompt)
		}
	}
}

// Unset, the floor and the fold cut are the skill's own defaults: the prompt
// names no value of its own for either.
func TestWithoutAFloorOrAFoldCutThePromptLeavesTheSkillsDefaults(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{})
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	prompt := f.model.asked[0].Prompt
	for _, want := range []string{"the skill's default severity floor", "its default fold cut"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not contain %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"severity floor of `", "fold cut of "} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("the prompt names a value for %q:\n%s", unwanted, prompt)
		}
	}
}

// An advisory review is append-only (#123): once posted it is never edited or
// deleted. A replayed transition - here verify, its commit lost after the
// review landed - runs the model again, and a different reply leaves the
// posted review as it was.
func TestAReplayedTransitionDoesNotRewriteAPostedReview(t *testing.T) {
	tr := newTracker(command(1))
	m := &reviewer{replies: []string{"The first reply.", "A different reply."}}
	f := setup(t, tr, m)
	f.run.Store = &losesCommit{Store: f.store, state: review.Start}

	f.drive()
	if len(m.asked) != 2 {
		t.Fatalf("the model ran %d times; the lost commit should have replayed the review", len(m.asked))
	}
	posted := tr.byAgent()
	if len(posted) != 1 {
		t.Fatalf("%d comments from the agent, want the one review", len(posted))
	}
	if b := posted[0].Body; !strings.Contains(b, "The first reply.") || strings.Contains(b, "A different reply.") {
		t.Errorf("the posted review was rewritten:\n%s", b)
	}
	if tr.posts != 1 {
		t.Errorf("posted %d times, want 1", tr.posts)
	}
	if j := f.now(); j.State != review.Start || !j.NextRunAt.IsZero() {
		t.Errorf("job = %+v, want it at rest", j)
	}
}

// cites is a checkout of head holding the files a reply's citations name.
func cites(files ...string) review.Checkout {
	return func(_ context.Context, dir string, _ int) (string, error) {
		for _, f := range append(files, ".git/afk-pr.diff") {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
				return "", err
			}
		}
		return head, nil
	}
}

// Each file:line the reply cites is posted as a permalink at the reviewed
// head, by the agent rather than the model (#110): a citation of a file in the
// checkout is linked, and nothing else is.
func TestACitationOfAFileAtTheHeadIsPostedAsAPermalink(t *testing.T) {
	reply := strings.Join([]string{
		"1. **should-fix**: `internal/x.go:42` is wrong.",
		"2. **blocker**: internal/x.go:7-9 and ./docs/y.md:3.",
		"3. Already a link: [internal/x.go:5](https://example.com/internal/x.go:5).",
		"4. Not in the checkout: internal/gone.go:3, and at 10:30, and `internal/x.go:0`.",
		"5. A span that is more than a citation: `f(internal/x.go:2)`.",
		"6. Not at head: .git/afk-pr.diff:4, internal:1.",
		"```",
		"internal/x.go:11",
		"```",
	}, "\n")
	f := setup(t, newTracker(command(1)), &reviewer{replies: []string{reply}})
	f.deps.Checkout = cites("internal/x.go", "docs/y.md")
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	b := f.tr.byAgent()[0].Body
	at := "https://github.com/owner/name/blob/" + head + "/"
	for _, want := range []string{
		"[`internal/x.go:42`](" + at + "internal/x.go#L42) is wrong",
		"[internal/x.go:7-9](" + at + "internal/x.go#L7-L9) and [./docs/y.md:3](" + at + "docs/y.md#L3).",
		"[internal/x.go:5](https://example.com/internal/x.go:5).",
		"Not in the checkout: internal/gone.go:3, and at 10:30, and `internal/x.go:0`.",
		"`f(internal/x.go:2)`",
		"Not at head: .git/afk-pr.diff:4, internal:1.",
		"```\ninternal/x.go:11\n```",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the review does not contain %q:\n%s", want, b)
		}
	}
	if n := strings.Count(b, at); n != 3 {
		t.Errorf("%d permalinks, want 3:\n%s", n, b)
	}
}

// With no repository there is nothing to build a permalink on, and the
// citations are posted as the model wrote them rather than as broken links.
func TestWithoutARepositoryCitationsArePostedAsWritten(t *testing.T) {
	f := setup(t, newTracker(command(1)), &reviewer{replies: []string{"See `internal/x.go:42`."}})
	f.deps.Checkout = cites("internal/x.go")
	f.deps.Repo = ""
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if b := f.tr.byAgent()[0].Body; !strings.Contains(b, "See `internal/x.go:42`.") || strings.Contains(b, "github.com") {
		t.Errorf("the citation was not posted as written:\n%s", b)
	}
}

// The revise job asks for a review of the head its revision left by making the
// job due, with no command: the reply it posted for that head is the request,
// claimed with a 👀 on the reply and never on the description, and the review
// links it. A reply for another head, and a human's comment carrying the
// marker, are not requests.
func TestTheRevisionsReplyIsARequest(t *testing.T) {
	reply := github.Comment{ID: 40, Login: agent, Body: owed.RevisionReplyMarker(12, head) + "\n## Points\n\n- done."}
	older := github.Comment{ID: 30, Login: agent, Body: owed.RevisionReplyMarker(12, "0ld") + "\n## Points\n\n- done."}
	forged := github.Comment{ID: 50, Login: "mallory", Body: owed.RevisionReplyMarker(12, head)}
	tr := newTracker(older, reply, forged)
	f := setup(t, tr, &reviewer{})

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if !tr.claimed(reply.ID) {
		t.Error("the reply for the head was not claimed")
	}
	if tr.claimed(older.ID) || tr.claimed(forged.ID) {
		t.Error("a reply for another head, or a human's comment, was claimed")
	}
	if len(tr.prEyes) != 0 {
		t.Errorf("the description was claimed: %v", tr.prEyes)
	}
	var posted []github.Comment
	for _, c := range tr.byAgent() {
		if strings.Contains(c.Body, review.Marker(head)) {
			posted = append(posted, c)
		}
	}
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "Asked for by the revise job, once CI was green, with [its reply](https://github.com/owner/name/pull/12#issuecomment-40).") {
		t.Fatalf("reviews = %+v, want one linking the reply that asked", posted)
	}

	// Claimed once: a /review later is a human's, and does not claim the
	// reply again.
	tr.comments = append(tr.comments, command(9))
	f.restart()
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if n := len(tr.reactions[reply.ID]); n != 1 {
		t.Errorf("%d reactions on the reply, want 1", n)
	}
}

// A revision that asks while the job is already on its way - here deferred on
// a human's /review - is left as it is, so the claim never saw its reply. The
// review it resumes into still claims the reply and links it.
func TestADeferredReviewClaimsTheReplyLeftWhileItWaited(t *testing.T) {
	tr := newTracker(command(1))
	f := setup(t, tr, &reviewer{answers: []error{transient(first), transient(second)}})
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if j := f.now(); j.State != review.Deferred {
		t.Fatalf("job = %+v, want it deferred", j)
	}

	reply := github.Comment{ID: 40, Login: agent, Body: owed.RevisionReplyMarker(12, head) + "\n## Points\n\n- done."}
	tr.comments = append(tr.comments, reply)
	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if !tr.claimed(reply.ID) {
		t.Error("the reply left while the review waited was not claimed")
	}
	var posted []github.Comment
	for _, c := range tr.byAgent() {
		if strings.Contains(c.Body, review.Marker(head)) {
			posted = append(posted, c)
		}
	}
	if len(posted) != 1 || !strings.Contains(posted[0].Body, "[its reply](https://github.com/owner/name/pull/12#issuecomment-40)") {
		t.Fatalf("reviews = %+v, want one linking the reply", posted)
	}
}

// The review a revision asks for covers what the operator's second sitting
// reads: the delta from the head the send-back was written against, which the
// reply says, to the head the revision left (#132). The diff file holds the
// delta and nothing else, the whole pull request's diff is beside it as
// context, and the summary names the range. The earlier review is left as it
// was: this one is a new comment, numbered from 1 again.
func TestARevisionsReviewIsOfItsDelta(t *testing.T) {
	earlier := github.Comment{ID: 20, Login: agent, Body: "<details>\n<summary>Advisory review of <code>fedcba9</code>.</summary>\n\n" + review.Marker(read) + "\n1. should-fix\n</details>\n"}
	reply := github.Comment{ID: 40, Login: agent, Body: owed.RevisionReplyMarker(12, head) + "\n" + owed.RevisionReadMarker(read) + "\n## Points\n\n- done."}
	tr := newTracker(earlier, reply)
	m := &reviewer{replies: []string{"## Correctness\n\n1. should-fix `store.go:1`: Release is untested."}}
	f := setup(t, tr, m)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if want := []string{base + "..." + head, read + "..." + head}; !slices.Equal(tr.compares, want) {
		t.Errorf("the diffs read were %v, want %v", tr.compares, want)
	}
	if len(m.diffs) != 1 || m.diffs[0] != delta {
		t.Errorf("the diff file held %q, want only the delta %q", m.diffs, delta)
	}
	if len(m.wholes) != 1 || m.wholes[0] != diff {
		t.Errorf("the whole pull request's diff was %q, want %q beside the delta", m.wholes, diff)
	}
	if p := m.asked[0].Prompt; !strings.Contains(p, read+".."+head) || !strings.Contains(p, ".git/afk-pr-whole.diff") {
		t.Errorf("the prompt does not name the range and the whole diff:\n%s", p)
	}

	got := tr.byAgent()
	if len(got) != 3 || got[0] != earlier || got[1] != reply {
		t.Fatalf("the agent's comments are %+v, want the earlier review and the reply untouched, and one more", got)
	}
	posted := got[2].Body
	if !strings.Contains(posted, review.Marker(head)) {
		t.Fatalf("the new comment is not the review of %s:\n%s", head, posted)
	}
	if want := "<summary>Advisory review of <code>" + git.Short(read) + ".." + git.Short(head) + "</code>. Open it after your own reading.</summary>"; !strings.Contains(posted, want) {
		t.Errorf("the summary does not name the range, and nothing else (%s):\n%s", want, posted)
	}
	if !strings.Contains(posted, "1. should-fix") {
		t.Errorf("the findings are not numbered from 1:\n%s", posted)
	}
}

// A review nobody asked for by a revision is of the pull request against its
// base, as before: a /review, and a revision whose reply does not say where
// its send-back was written, which a reply posted before replies said so does
// not.
func TestAReviewNotOfARevisionIsOfThePullRequest(t *testing.T) {
	for name, c := range map[string]github.Comment{
		"a /review":            command(1),
		"a reply with no read": {ID: 40, Login: agent, Body: owed.RevisionReplyMarker(12, head) + "\n## Points\n\n- done."},
	} {
		t.Run(name, func(t *testing.T) {
			tr := newTracker(c)
			m := &reviewer{}
			f := setup(t, tr, m)
			if errs := f.drive(); len(errs) != 0 {
				t.Fatalf("errors: %v", errs)
			}
			if want := []string{base + "..." + head}; !slices.Equal(tr.compares, want) {
				t.Errorf("the diffs read were %v, want %v: the pull request's alone", tr.compares, want)
			}
			if len(m.diffs) != 1 || m.diffs[0] != diff || m.wholes[0] != "" {
				t.Errorf("the diff file held %q and the whole diff %q, want the pull request's diff alone", m.diffs, m.wholes)
			}
			if p := m.asked[0].Prompt; strings.Contains(p, "afk-pr-whole.diff") {
				t.Errorf("the prompt names a whole diff:\n%s", p)
			}
			var posted []string
			for _, c := range tr.byAgent() {
				if strings.Contains(c.Body, review.Marker(head)) {
					posted = append(posted, c.Body)
				}
			}
			if len(posted) != 1 || !strings.Contains(posted[0], "<summary>Advisory review of <code>"+git.Short(head)+"</code>.") {
				t.Errorf("the reviews are %q, want one whose summary names the head alone", posted)
			}
		})
	}
}

// comparing is the fixture tracker with its diffs read by a real client, from
// a server whose pull request endpoint refuses the diff as GitHub does past
// 300 files, and whose compare serves it.
type comparing struct {
	*tracker
	client *github.Client
}

func (c comparing) Compare(ctx context.Context, from, to string) (string, error) {
	return c.client.Compare(ctx, from, to)
}

// A pull request too large for the pull request endpoint's diff is reviewed
// all the same: its diff is read through compare, from its base, which has no
// cap on the files it covers (#176).
func TestAPullRequestPastTheDiffCapIsReviewed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/name/pulls/12":
			w.WriteHeader(http.StatusNotAcceptable)
			fmt.Fprint(w, `{"message":"Sorry, the diff exceeded the maximum number of files (300).","errors":[{"resource":"PullRequest","field":"diff","code":"too_large"}]}`)
		case "/repos/owner/name/compare/" + base + "..." + head:
			fmt.Fprint(w, diff)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	tr := newTracker(command(1))
	m := &reviewer{}
	f := setup(t, tr, m)
	f.deps.Tracker = comparing{tr, &github.Client{Repo: "owner/name", BaseURL: srv.URL}}

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(m.diffs) != 1 || m.diffs[0] != diff {
		t.Errorf("the diff file held %q, want the pull request's diff %q", m.diffs, diff)
	}
	if posted := tr.byAgent(); len(posted) != 1 || !strings.Contains(posted[0].Body, review.Marker(head)) {
		t.Errorf("agent comments = %+v, want the review of %s", posted, head)
	}
}

// A revision's delta is reviewed against the send-back it answers: the
// operator's commands, verbatim, which the reply names in its hidden lines.
// The issue the pull request closes follows as background, since the delta
// takes on the points and not the whole issue.
func TestARevisionsReviewIsOfItsSendBack(t *testing.T) {
	point := github.Comment{ID: 30, Login: "operator", Body: "/revise\nRelease is never called."}
	reply := github.Comment{ID: 40, Login: agent, Body: owed.RevisionReplyMarker(12, head) + "\n" + owed.RevisionReadMarker(read) + "\n" +
		owed.RevisionMarker(30) + "\n" + owed.PullRequestReviewRevisionMarker(50) + "\n" + owed.PullRequestReviewRevisionMarker(51) + "\n## Points\n\n- done."}
	tr := newTracker(point, reply)
	tr.desc = "Closes #7."
	tr.issues = map[int]github.Issue{7: {Number: 7, Title: "Reserve jobs", Body: "A job is reserved before it runs."}}
	tr.reviews = []github.PullRequestReview{{ID: 50, Login: "operator", Body: "/revise name the lease"}}
	tr.lines = map[int64][]github.LineComment{50: {{ID: 60, Body: "This leaks the lease.", Path: "store.go", Line: 3, CommitID: read}}}
	m := &reviewer{}
	f := setup(t, tr, m)

	if errs := f.drive(); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	spec := m.specs[0]
	order := []string{"/revise\nRelease is never called.", "/revise name the lease", "`store.go` line 3", "This leaks the lease.", "review 51", "could not be read", "Closes #7.", "A job is reserved before it runs."}
	at := 0
	for _, want := range order {
		i := strings.Index(spec[at:], want)
		if i < 0 {
			t.Fatalf("the spec does not have %q after what comes before it:\n%s", want, spec)
		}
		at += i + len(want)
	}
	if p := m.asked[0].Prompt; !strings.Contains(p, "send-back") {
		t.Errorf("the prompt does not say the spec is the send-back:\n%s", p)
	}
}
