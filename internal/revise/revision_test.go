package revise_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/handoff"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

var (
	refFirst  = model.Ref{Provider: "opencode-go", Model: "first"}
	refSecond = model.Ref{Provider: "opencode-go", Model: "second"}
)

// reviser is a fake model. Each run does what the next of its turns says, in
// the workspace, and succeeds.
type reviser struct {
	mu    sync.Mutex
	turns []func(dir string) error
	asked []opencode.Request

	// cost is what each run reports it spent, with a thousand tokens in
	// and a hundred out, whether its turn failed or not. Zero reports
	// nothing.
	cost float64

	// lastInput is the last-turn input each run reports, whether its turn
	// failed or not.
	lastInput int
}

func (m *reviser) spent() opencode.Reply {
	if m.cost == 0 {
		return opencode.Reply{}
	}
	return opencode.Reply{Cost: m.cost, Tokens: opencode.Tokens{Input: 1000, Output: 100}}
}

func (m *reviser) Run(_ context.Context, req opencode.Request) (opencode.Reply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, req)
	if len(m.turns) > 0 {
		turn := m.turns[0]
		m.turns = m.turns[1:]
		if err := turn(req.Dir); err != nil {
			reply := m.spent()
			reply.LastInput = m.lastInput
			return reply, err
		}
	}
	reply := m.spent()
	reply.Text, reply.Session, reply.LastInput = "Done.", "ses_1", m.lastInput
	return reply, nil
}

func (m *reviser) then(turns ...func(dir string) error) { m.turns = append(m.turns, turns...) }

// commitOn is a turn that writes name with content, commits it, and writes the
// gate's `ok` file so the fixture's gate passes.
func commitOn(name string) func(dir string) error {
	return func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "ok"), []byte("ok\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", name, "ok"); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "revise: "+name)
		return err
	}
}

// seed commits a file in the fixture repository, without the gate's `ok`: the
// gate is the revision's, not the seed's.
func seed(name string) func(dir string) error {
	return func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", name); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "add "+name)
		return err
	}
}

// advisor is a fake model for the review job the revision asks for.
type advisor struct{}

func (advisor) Run(context.Context, opencode.Request) (opencode.Reply, error) {
	return opencode.Reply{Text: "1. Nothing to add."}, nil
}

// revise is one session run: it commits name and writes the reply.
func reviseOn(name, reply string) func(dir string) error {
	return func(dir string) error {
		if err := commitOn(name)(dir); err != nil {
			return err
		}
		return replyOn(reply)(dir)
	}
}

// replyOn is a turn that writes the session's part of the revision's reply.
func replyOn(body string) func(dir string) error {
	return func(dir string) error {
		return os.WriteFile(filepath.Join(dir, ".git", "afk-reply.md"), []byte(body), 0o644)
	}
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// revisionRemote is a bare repository with a `main` branch and a `feature`
// branch one commit ahead of it, standing in for the tracker's repository. It
// returns the remote path and feature's commit.
func revisionRemote(t *testing.T) (remote, head string) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "afk", "GIT_AUTHOR_EMAIL": "afk@example.invalid",
		"GIT_COMMITTER_NAME": "afk", "GIT_COMMITTER_EMAIL": "afk@example.invalid",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	remote = filepath.Join(dir, "remote.git")
	seedDir := filepath.Join(dir, "seed")
	for _, step := range [][]string{
		{"git", "init", "--quiet", "--bare", "--initial-branch=main", remote},
		{"git", "clone", "--quiet", remote, seedDir},
	} {
		if _, err := run(dir, step[0], step[1:]...); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed("README")(seedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := run(seedDir, "git", "push", "--quiet", "origin", "HEAD:main"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(seedDir, "git", "switch", "--quiet", "--create", "feature"); err != nil {
		t.Fatal(err)
	}
	if err := seed("feature.txt")(seedDir); err != nil {
		t.Fatal(err)
	}
	if _, err := run(seedDir, "git", "push", "--quiet", "origin", "feature"); err != nil {
		t.Fatal(err)
	}
	head, err := run(seedDir, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return remote, head
}

type revFixture struct {
	t       *testing.T
	tr      *tracker
	model   *reviser
	deps    *revise.Deps
	remote  string
	feature string
	head    string

	store  store.Store
	reg    *transition.Registry
	runner *transition.Runner

	// at is the runner's clock, and logs what the kind logged.
	at   time.Time
	logs []string
}

func jobID() string {
	return store.ID(store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12})
}

// setupRevision is pull request 12 on a fixture tracker, its branch `feature`
// on a local bare remote, and a revision job claimed: the state #146 starts
// from.
func setupRevision(t *testing.T) *revFixture {
	t.Helper()
	f := revisionFixture(t)
	f.tr.say(12, send(1, "/revise Rename Foo to Bar."))
	f.claim()
	return f
}

// revisionFixture is setupRevision before anything is sent back or claimed.
func revisionFixture(t *testing.T) *revFixture {
	t.Helper()
	remote, head := revisionRemote(t)
	tr := newTracker()
	tr.pr.HeadSHA = head
	tr.pr.HeadRef = "feature"
	s := storetest.Open(t)
	m := &reviser{}
	state := t.TempDir()
	d := &revise.Deps{
		Tracker: tr,
		Model:   m,
		Store:   s,
		Login:   agent,
		Repo:    repo,
		// As the command surface builds it: the remote is never reached
		// from a workspace.
		Remote:        git.Remote{URL: remote, Untrusted: []string{filepath.Join(state, "workspaces")}},
		Resolve:       func(context.Context) (model.Candidates, error) { return model.Candidates{refFirst, refSecond}, nil },
		Bound:         2,
		TierWait:      time.Hour,
		Rounds:        3,
		Gate:          "echo checking; test -f ok || { echo 'FAIL: no ok' >&2; exit 1; }",
		Attempts:      3,
		Denylist:      []string{"flake.lock", ".github/**", "**/secrets.yaml"},
		CIWait:        10 * time.Minute,
		CICeiling:     2 * time.Hour,
		CIFixes:       2,
		Replays:       2,
		HandOffLabel:  handOff,
		HandBackLabel: "needs-decision",
		StateDir:      state,
	}
	// The review job the revision asks for, beside it, as the pool runs both.
	rd := &review.Deps{
		Tracker: tr, Model: advisor{}, Store: s,
		Checkout: func(ctx context.Context, dir string, n int) (string, error) {
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
				return "", err
			}
			pr, err := tr.PullRequest(ctx, n)
			return pr.HeadSHA, err
		},
		Resolve:  d.Resolve,
		Bound:    1,
		Rounds:   2,
		TierWait: time.Hour,
		Repo:     repo, Login: agent, HandBackLabel: d.HandBackLabel, StateDir: state,
	}
	d.AskReview = handoff.Asker(transition.Armer{Store: s, Holder: "ask-review", LeaseTTL: time.Minute})
	reg := transition.MustRegistry(append(revise.Transitions(d), review.Transitions(rd)...)...)
	f := &revFixture{t: t, tr: tr, model: m, deps: d, remote: remote, feature: "feature", head: head, store: s, reg: reg, at: now}
	d.Log = func(msg string) { f.logs = append(f.logs, msg) }
	f.runner = &transition.Runner{Store: s, Registry: reg, Holder: "test", LeaseTTL: time.Minute, Clock: func() time.Time { return f.at }}
	return f
}

// claim arms and claims the send-back, leaving the job due in revising.
func (f *revFixture) claim() {
	f.t.Helper()
	ctx := context.Background()
	in := &intake.Intake{
		Tracker: f.tr, Store: f.store, Login: agent, Holder: "intake", LeaseTTL: time.Minute,
		Clock:    func() time.Time { return now },
		Commands: []intake.Command{{Word: revise.Word, On: store.SubjectPR, Kind: store.KindRevise, Start: revise.Start, ByReview: true}},
	}
	if _, err := in.Pass(ctx); err != nil {
		f.t.Fatal(err)
	}
	f.step(revise.Revising)
}

// step runs transitions until the job reaches state, or has no move from where
// it is. An error from a transition is recorded and the loop carries on: a
// failed effect leaves the state committed, and the next transition is what
// reads it back.
func (f *revFixture) step(state string) store.Job {
	f.t.Helper()
	ctx := context.Background()
	for range 30 {
		job, err := f.store.Job(ctx, jobID())
		if err != nil {
			f.t.Fatal(err)
		}
		if job.State == state {
			return job
		}
		next, ok := f.reg.Next(job.Kind, job.State)
		if !ok || job.NextRunAt.IsZero() {
			return job
		}
		f.runner.Run(ctx, next.Name, jobID())
	}
	f.t.Fatalf("the job never reached %q", state)
	return store.Job{}
}

// drive runs the revision up to its reply: reply_test.go takes it from there,
// with the review job beside it.
func (f *revFixture) drive() store.Job { return f.step(revise.Replying) }

func (f *revFixture) now() store.Job {
	f.t.Helper()
	job, err := f.store.Job(context.Background(), jobID())
	if err != nil {
		f.t.Fatal(err)
	}
	return job
}

// progress is what the job's own progress file holds.
func (f *revFixture) progress() (points []int64, reply string) {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.deps.StateDir, "progress", jobID()+".json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var p struct {
		Points []int64 `json:"points"`
		Reply  string  `json:"reply"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		f.t.Fatal(err)
	}
	return p.Points, p.Reply
}

// remoteHead is the commit the remote's feature branch is at.
func (f *revFixture) remoteHead() string {
	f.t.Helper()
	out, err := run(f.remote, "git", "rev-parse", "feature")
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// ancestor reports whether a is an ancestor of b on the remote.
func (f *revFixture) ancestor(a, b string) bool {
	f.t.Helper()
	out, err := run(f.remote, "git", "merge-base", "--is-ancestor", a, b)
	_ = out
	return err == nil
}

// A revision adds commits on top of the head the send-back was written
// against, and the push has that head as an ancestor.
func TestARevisionAddsCommitsOnTopOfTheSendBacksHead(t *testing.T) {
	f := setupRevision(t)
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo to Bar.\" done in `deadbee`."))

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Replying)
	}
	at := f.remoteHead()
	if at == f.head {
		t.Fatalf("the remote's feature branch is still at the send-back's head %s: nothing was pushed", git.Short(f.head))
	}
	if !f.ancestor(f.head, at) {
		t.Errorf("the send-back's head %s is not an ancestor of the pushed head %s", git.Short(f.head), git.Short(at))
	}
}

// A point the session declines is not done, and the other points still are:
// the revision is pushed, and the reply the session wrote is kept. The session
// is given every point, in the send-back's order, and the issue the pull
// request is for.
func TestADeclinedPointIsKeptAndTheRevisionGoesOn(t *testing.T) {
	f := revisionFixture(t)
	f.tr.pr.Body = "Closes #7.\n\n<!-- afk:implement issue=7 -->"
	f.tr.issues[7] = github.Issue{Number: 7, State: "open", Title: "Rename the widget", Body: "Foo is a bad name."}
	f.tr.say(12, send(1, "/revise Rename Foo to Bar."))
	f.tr.say(12, send(2, "/revise And drop the flag."))
	f.claim()
	var spec string
	f.model.then(func(dir string) error {
		b, err := os.ReadFile(filepath.Join(dir, ".git", "afk-send-back.md"))
		if err != nil {
			return err
		}
		spec = string(b)
		return reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done in abc123.\n- \"And drop the flag\" not done: the flag is load-bearing for the parser.\n")(dir)
	})

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s: a declined point does not stop the revision", job.State, revise.Replying)
	}
	first, second := strings.Index(spec, "Rename Foo to Bar."), strings.Index(spec, "And drop the flag.")
	if first < 0 || second < first {
		t.Errorf("the send-back given to the session does not hold both points in order:\n%s", spec)
	}
	if !strings.Contains(spec, "# Issue #7: Rename the widget") || !strings.Contains(spec, "Foo is a bad name.") {
		t.Errorf("the send-back given to the session does not hold the linked issue:\n%s", spec)
	}
	points, reply := f.progress()
	if len(points) != 2 || points[0] != 1 || points[1] != 2 {
		t.Errorf("the revision answers commands %v, want [1 2]", points)
	}
	if !strings.Contains(reply, "not done: the flag is load-bearing") || !strings.Contains(reply, "\"Rename Foo\" done") {
		t.Errorf("the reply kept is %q, want the done point and the declined point's reason", reply)
	}
	at := f.remoteHead()
	if at == f.head || !f.ancestor(f.head, at) {
		t.Errorf("the other point was not pushed: the remote is at %s", git.Short(at))
	}
	if _, err := run(f.remote, "git", "cat-file", "-e", at+":bar.txt"); err != nil {
		t.Errorf("the pushed head has no bar.txt, the done point's commit: %v", err)
	}
}

// A push by someone else during the revision is never overwritten: the lease
// refuses, and with no replay left the job hands back on the pull request.
func TestAPushBySomeoneElseIsNotOverwritten(t *testing.T) {
	f := setupRevision(t)
	f.deps.Replays = 0
	// The session commits its revision, and someone else pushes a different
	// commit to the branch while it runs.
	f.model.then(func(dir string) error {
		if err := reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done in abc123.\n")(dir); err != nil {
			return err
		}
		return f.someoneElsePushes("other.txt")
	})

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q (due %v), want at rest in start after a hand-back", job.State, !job.NextRunAt.IsZero())
	}
	at := f.remoteHead()
	if at == f.head {
		t.Error("the remote's feature branch did not move: the other push was overwritten or never read")
	}
	if !f.handedBack() {
		t.Error("no hand-back label on the pull request")
	}
	body := f.handBack()
	if body == "" {
		t.Fatal("no hand-back comment on the pull request")
	}
	// It says where the branch is and where the send-back was written, once
	// each, and lists the points done so far.
	want := fmt.Sprintf("it is at `%s`, not at `%s` where the send-back was written.", git.Short(at), git.Short(f.head))
	if !strings.Contains(body, want) || strings.Contains(body, "at `it is") {
		t.Errorf("the hand-back does not say %q plainly:\n%s", want, body)
	}
	if !strings.Contains(body, "\"Rename Foo\" done in abc123.") {
		t.Errorf("the hand-back does not list the points done so far:\n%s", body)
	}
}

// someoneElsePushes makes a divergent commit on the remote's feature branch,
// standing in for anyone pushing while the revision runs.
func (f *revFixture) someoneElsePushes(name string) error {
	return pushAs(f.t.TempDir(), f.remote, name, name+"\n")
}

// pushAs clones remote into dir, writes name with content on its feature
// branch, and pushes the commit: someone other than the agent pushing.
func pushAs(dir, remote, name, content string) error {
	if _, err := run(dir, "git", "clone", "--quiet", remote, dir); err != nil {
		return err
	}
	if _, err := run(dir, "git", "switch", "--quiet", "feature"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		return err
	}
	if _, err := run(dir, "git", "add", name); err != nil {
		return err
	}
	if _, err := run(dir, "git", "commit", "--quiet", "-m", "someone else: "+name); err != nil {
		return err
	}
	_, err := run(dir, "git", "push", "--quiet", "origin", "feature")
	return err
}

func (f *revFixture) handedBack() bool {
	for _, l := range f.tr.pr.Labels {
		if l == "needs-decision" {
			return true
		}
	}
	return false
}

// handBack is the revision's hand-back the agent posted, or empty.
func (f *revFixture) handBack() string {
	for _, c := range f.tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, "afk:revision-hand-back") {
			return c.Body
		}
	}
	return ""
}

// The denylist runs before every push, and a denied path hands back on the
// pull request rather than pushing.
func TestTheDenylistHandsBackBeforeThePush(t *testing.T) {
	f := setupRevision(t)
	f.model.then(commitOn("flake.lock"), replyOn("## Points\n\n- \"Rename Foo\" done."))

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start", job.State)
	}
	if at := f.remoteHead(); at != f.head {
		t.Errorf("the remote moved to %s, want the send-back's head: a denied path was pushed", git.Short(at))
	}
	if !f.handedBack() {
		t.Error("no hand-back on the pull request")
	}
}

// The gate's retries are the implement kind's: it may fail a bounded number of
// times, and then the revision is handed back.
func TestTheGateRetriesThenHandsBack(t *testing.T) {
	f := setupRevision(t)
	// The gate fails: no `ok` file is committed.
	f.model.then(
		func(dir string) error {
			if err := os.WriteFile(filepath.Join(dir, "bar.txt"), []byte("bar\n"), 0o644); err != nil {
				return err
			}
			if _, err := run(dir, "git", "add", "bar.txt"); err != nil {
				return err
			}
			_, err := run(dir, "git", "commit", "--quiet", "-m", "revise: bar")
			return err
		},
	)

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start", job.State)
	}
	if at := f.remoteHead(); at != f.head {
		t.Errorf("the remote moved to %s: a failed gate was pushed", git.Short(at))
	}
	if !f.handedBack() {
		t.Error("no hand-back on the pull request")
	}
	// One session, then one retry for each failure the bound allows, each
	// continuing the session that wrote the commits, with the failure.
	if len(f.model.asked) != f.deps.Attempts {
		t.Fatalf("%d model runs, want %d: one for each attempt the gate is allowed", len(f.model.asked), f.deps.Attempts)
	}
	for i, req := range f.model.asked[1:] {
		if req.Session != "ses_1" {
			t.Errorf("retry %d ran in session %q, want the session that wrote the commits", i+1, req.Session)
		}
		if !strings.Contains(req.Prompt, "afk-gate.log") {
			t.Errorf("retry %d was not pointed at the gate's failure:\n%s", i+1, req.Prompt)
		}
	}
	if body := f.handBack(); !strings.Contains(body, fmt.Sprintf("after %d attempts", f.deps.Attempts)) || !strings.Contains(body, "FAIL: no ok") {
		t.Errorf("the hand-back does not say the gate ran out, with its output:\n%s", body)
	}
}

// A transient model failure moves to the next candidate, as for implement.
func TestATransientFailureMovesToTheNextCandidate(t *testing.T) {
	f := setupRevision(t)
	f.model.then(func(string) error {
		return &opencode.TransientError{Model: refFirst, Err: errors.New("429 Too Many Requests")}
	})
	f.model.then(commitOn("bar.txt"), replyOn("## Points\n\n- \"Rename Foo\" done."))

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Replying)
	}
	if len(f.model.asked) < 2 {
		t.Fatalf("%d model runs, want the tier retried", len(f.model.asked))
	}
	if f.model.asked[0].Model == f.model.asked[1].Model {
		t.Errorf("both runs used %v, want the next candidate", f.model.asked[0].Model)
	}
}

// The session is fresh, and the send-back it is given holds the points and the
// diff at the head they were written against.
func TestTheSessionIsGivenThePointsAndTheDiffFresh(t *testing.T) {
	f := setupRevision(t)
	var spec string
	f.model.then(func(dir string) error {
		b, err := os.ReadFile(filepath.Join(dir, ".git", "afk-send-back.md"))
		if err != nil {
			return err
		}
		spec = string(b)
		return reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done.")(dir)
	})

	f.drive()
	if !strings.Contains(spec, "## The points") || !strings.Contains(spec, "Rename Foo to Bar.") {
		t.Errorf("the send-back given to the session is %q, want the points", spec)
	}
	if !strings.Contains(spec, "```diff") {
		t.Errorf("the send-back given to the session is %q, want the diff", spec)
	}
	if len(f.model.asked) == 0 || f.model.asked[0].Session != "" {
		t.Errorf("the first run named a session, want a fresh one: %+v", f.model.asked[0].Session)
	}
}

// A revision adds commits on top of the head the send-back was written
// against, never rewriting it: a session that rebases it away hands back.
func TestRewritingTheHeadTheSendBackWasWrittenAgainstHandsBack(t *testing.T) {
	f := setupRevision(t)
	f.model.then(func(dir string) error {
		if _, err := run(dir, "git", "reset", "--quiet", "--hard", "HEAD~1"); err != nil {
			return err
		}
		return reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done.")(dir)
	})

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start", job.State)
	}
	if at := f.remoteHead(); at != f.head {
		t.Errorf("the remote moved to %s: a rewritten history was pushed", git.Short(at))
	}
	if !f.handedBack() {
		t.Error("no hand-back on the pull request")
	}
	// Handed back at the gate, before an attempt is spent on a retry.
	if n := len(f.model.asked); n != 1 {
		t.Errorf("%d model runs, want 1: a rewritten head is not a gate failure to retry", n)
	}
	if body := f.handBack(); !strings.Contains(body, "The revision rewrote `"+git.Short(f.head)+"`") {
		t.Errorf("the hand-back does not say the head was rewritten:\n%s", body)
	}
}

// A revision's hand-back carries the send-back's markers for each command, so
// a command written after the hand-back reads as in flight (#145).
func TestAHandBackCarriesTheRevisionsMarkers(t *testing.T) {
	f := setupRevision(t)
	// The gate fails, so the revision is handed back with no push.
	f.model.then(func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, "bar.txt"), []byte("bar\n"), 0o644); err != nil {
			return err
		}
		if _, err := run(dir, "git", "add", "bar.txt"); err != nil {
			return err
		}
		_, err := run(dir, "git", "commit", "--quiet", "-m", "revise: bar")
		return err
	})

	f.drive()
	for _, c := range f.tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, "afk:revision-hand-back") {
			if !strings.Contains(c.Body, owed.RevisionMarker(1)) {
				t.Errorf("the hand-back does not carry the revision's marker for comment 1:\n%s", c.Body)
			}
			return
		}
	}
	t.Fatal("no hand-back comment on the pull request")
}

// A session that fails transiently after committing leaves nothing for the
// next candidate: it starts again from the send-back's head, and writes its
// own reply.
func TestATransientFailureLeavesNothingForTheNextCandidate(t *testing.T) {
	f := setupRevision(t)
	f.model.then(func(dir string) error {
		if err := reviseOn("half.txt", "## Points\n\n- stale")(dir); err != nil {
			return err
		}
		return &opencode.TransientError{Model: refFirst, Err: errors.New("stream reset")}
	})
	var started string
	f.model.then(func(dir string) error {
		var err error
		if started, err = run(dir, "git", "rev-parse", "HEAD"); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, ".git", "afk-reply.md")); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("the failed session's reply is still there: %v", err)
		}
		return reviseOn("bar.txt", "## Points\n\n- fresh")(dir)
	})

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Replying)
	}
	if started != f.head {
		t.Errorf("the next candidate started at %s, want the send-back's head %s", git.Short(started), git.Short(f.head))
	}
	at := f.remoteHead()
	if _, err := run(f.remote, "git", "cat-file", "-e", at+":half.txt"); err == nil {
		t.Error("the failed session's commit was pushed")
	}
	if _, reply := f.progress(); reply != "## Points\n\n- fresh" {
		t.Errorf("the reply kept is %q, want the session that finished's", reply)
	}
}

// The denylist is checked on what the revision adds. A path the pull request
// already touched, in a commit that is on the remote, is not the agent's push.
func TestTheDenylistReadsOnlyWhatTheRevisionAdds(t *testing.T) {
	f := revisionFixture(t)
	f.head = f.humanPushes("flake.lock")
	f.tr.pr.HeadSHA = f.head
	f.tr.say(12, send(1, "/revise Rename Foo to Bar."))
	f.claim()
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s: the pull request's own flake.lock is not the revision's\n%s", job.State, revise.Replying, f.handBack())
	}
	if at := f.remoteHead(); at == f.head || !f.ancestor(f.head, at) {
		t.Errorf("the revision was not pushed on top of %s: the remote is at %s", git.Short(f.head), git.Short(at))
	}
}

// A branch deleted between the claim and the revision is handed back rather
// than failed at for ever.
func TestABranchDeletedBeforeTheRevisionHandsBack(t *testing.T) {
	f := setupRevision(t)
	if _, err := run(f.remote, "git", "update-ref", "-d", "refs/heads/feature"); err != nil {
		t.Fatal(err)
	}

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start after a hand-back", job.State)
	}
	if len(f.model.asked) != 0 {
		t.Errorf("%d model runs, want none", len(f.model.asked))
	}
	if body := f.handBack(); !strings.Contains(body, "was deleted") || !strings.Contains(body, owed.RevisionMarker(1)) {
		t.Errorf("the hand-back does not say the branch was deleted, with the revision's marker:\n%s", body)
	}
}

// A branch pushed over between the claim and the revision, so that the head
// the send-back was written against is gone from it, is handed back too.
func TestABranchPushedOverBeforeTheRevisionHandsBack(t *testing.T) {
	f := setupRevision(t)
	if _, err := run(f.remote, "git", "update-ref", "refs/heads/feature", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q, want at rest in start after a hand-back", job.State)
	}
	if len(f.model.asked) != 0 {
		t.Errorf("%d model runs, want none", len(f.model.asked))
	}
	if body := f.handBack(); !strings.Contains(body, "is no longer on the branch") {
		t.Errorf("the hand-back does not say the head is gone:\n%s", body)
	}
}

// A send-back claimed before the claim recorded the branch (#145) is revised
// on the pull request's branch.
func TestASendBackWithNoBranchIsRevisedOnThePullRequests(t *testing.T) {
	f := setupRevision(t)
	path := filepath.Join(f.deps.StateDir, "send-backs", jobID()+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sb map[string]any
	if err := json.Unmarshal(b, &sb); err != nil {
		t.Fatal(err)
	}
	delete(sb, "ref")
	if b, err = json.Marshal(sb); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	f.model.then(reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done."))

	job := f.drive()
	if job.State != revise.Replying {
		t.Fatalf("the job is in %q, want %s\n%s", job.State, revise.Replying, f.handBack())
	}
	if at := f.remoteHead(); at == f.head || !f.ancestor(f.head, at) {
		t.Errorf("the revision was not pushed to feature: it is at %s", git.Short(at))
	}
}

// A send-back that is there and cannot be read is an error for the host, not a
// hand-back saying the state directory was wiped.
func TestAnUnreadableSendBackIsNotHandedBackAsLost(t *testing.T) {
	f := setupRevision(t)
	path := filepath.Join(f.deps.StateDir, "send-backs", jobID()+".json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	job := f.drive()
	if job.State != revise.Revising {
		t.Errorf("the job is in %q, want still %s", job.State, revise.Revising)
	}
	if body := f.handBack(); body != "" {
		t.Errorf("the revision was handed back:\n%s", body)
	}
	if len(f.model.asked) != 0 {
		t.Errorf("%d model runs, want none", len(f.model.asked))
	}
}

// humanPushes commits name on the remote's feature branch, as the pull
// request's author, and returns the new head.
func (f *revFixture) humanPushes(name string) string {
	f.t.Helper()
	if err := f.someoneElsePushes(name); err != nil {
		f.t.Fatal(err)
	}
	return f.remoteHead()
}
