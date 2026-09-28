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
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/owed"
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
}

func (m *reviser) Run(_ context.Context, req opencode.Request) (opencode.Reply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, req)
	if len(m.turns) > 0 {
		turn := m.turns[0]
		m.turns = m.turns[1:]
		if err := turn(req.Dir); err != nil {
			return opencode.Reply{}, err
		}
	}
	return opencode.Reply{Text: "Done.", Session: "ses_1"}, nil
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
}

func jobID() string {
	return store.ID(store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12})
}

// setupRevision is pull request 12 on a fixture tracker, its branch `feature`
// on a local bare remote, and a revision job claimed: the state #146 starts
// from.
func setupRevision(t *testing.T) *revFixture {
	t.Helper()
	remote, head := revisionRemote(t)
	tr := newTracker()
	tr.pr.HeadSHA = head
	tr.pr.HeadRef = "feature"
	s := storetest.Open(t)
	m := &reviser{}
	d := &revise.Deps{
		Tracker:       tr,
		Model:         m,
		Store:         s,
		Login:         agent,
		Repo:          repo,
		Remote:        git.Remote{URL: remote},
		Resolve:       func(context.Context) (model.Candidates, error) { return model.Candidates{refFirst, refSecond}, nil },
		Bound:         2,
		TierWait:      time.Hour,
		Rounds:        3,
		Gate:          "echo checking; test -f ok || { echo 'FAIL: no ok' >&2; exit 1; }",
		Attempts:      3,
		Denylist:      []string{"flake.lock", ".github/**", "**/secrets.yaml"},
		HandOffLabel:  handOff,
		HandBackLabel: "needs-decision",
		StateDir:      t.TempDir(),
	}
	reg := transition.MustRegistry(revise.Transitions(d)...)
	f := &revFixture{
		t: t, tr: tr, model: m, deps: d, remote: remote, feature: "feature", head: head, store: s, reg: reg,
		runner: &transition.Runner{Store: s, Registry: reg, Holder: "test", LeaseTTL: time.Minute, Clock: func() time.Time { return now }},
	}

	// The send-back, claimed the way #145 does, so the revision starts from
	// what a claim hands it.
	f.tr.say(12, send(1, "/revise Rename Foo to Bar."))
	f.claim()
	return f
}

// claim arms and claims the send-back, leaving the job due in revising.
func (f *revFixture) claim() {
	f.t.Helper()
	ctx := context.Background()
	in := &intake.Intake{
		Tracker: f.tr, Store: f.store, Login: agent, Holder: "intake", LeaseTTL: time.Minute,
		Clock:    func() time.Time { return now },
		Commands: []intake.Command{{Word: revise.Word, On: store.SubjectPR, Kind: store.KindRevise, Start: revise.Start}},
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

func (f *revFixture) drive() store.Job { return f.step("") }

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
	if job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Watching)
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
// the revision is pushed, and the reply the session wrote is kept.
func TestADeclinedPointIsKeptAndTheRevisionGoesOn(t *testing.T) {
	f := setupRevision(t)
	f.model.then(
		reviseOn("bar.txt", "## Points\n\n- \"Rename Foo\" done in abc123.\n- \"And drop the flag\" not done: the flag is load-bearing for the parser.\n"),
	)

	job := f.drive()
	if job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s: a declined point does not stop the revision", job.State, revise.Watching)
	}
	_, reply := f.progress()
	if !strings.Contains(reply, "not done: the flag is load-bearing") {
		t.Errorf("the reply kept is %q, want the declined point's reason", reply)
	}
	if at := f.remoteHead(); !f.ancestor(f.head, at) {
		t.Errorf("the other point was not pushed: the remote is at %s", git.Short(at))
	}
}

// A push by someone else during the revision is never overwritten: the lease
// refuses, and the job hands back on the pull request.
func TestAPushBySomeoneElseIsNotOverwritten(t *testing.T) {
	f := setupRevision(t)
	// The session commits its revision, and someone else pushes a different
	// commit to the branch while it runs.
	f.model.then(func(dir string) error {
		if err := commitOn("bar.txt")(dir); err != nil {
			return err
		}
		return f.someoneElsePushes("other.txt")
	})

	job := f.drive()
	if job.State != revise.Start || !job.NextRunAt.IsZero() {
		t.Fatalf("the job is in %q (due %v), want at rest in start after a hand-back", job.State, !job.NextRunAt.IsZero())
	}
	if at := f.remoteHead(); at == f.head {
		t.Error("the remote's feature branch did not move: the other push was overwritten or never read")
	}
	if !f.handedBack() {
		t.Error("no hand-back label on the pull request")
	}
	if !f.handBackComment() {
		t.Error("no hand-back comment on the pull request")
	}
}

// someoneElsePushes makes a divergent commit on the remote's feature branch,
// standing in for anyone pushing while the revision runs.
func (f *revFixture) someoneElsePushes(name string) error {
	dir := f.t.TempDir()
	if _, err := run(dir, "git", "clone", "--quiet", f.remote, dir); err != nil {
		return err
	}
	if _, err := run(dir, "git", "switch", "--quiet", "feature"); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
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

// handBackComment reports whether the agent posted a revision's hand-back.
func (f *revFixture) handBackComment() bool {
	for _, c := range f.tr.comments[12] {
		if c.Login == agent && strings.Contains(c.Body, "afk:revision-hand-back") {
			return true
		}
	}
	return false
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
}

// A transient model failure moves to the next candidate, as for implement.
func TestATransientFailureMovesToTheNextCandidate(t *testing.T) {
	f := setupRevision(t)
	f.model.then(func(string) error {
		return &opencode.TransientError{Model: refFirst, Err: errors.New("429 Too Many Requests")}
	})
	f.model.then(commitOn("bar.txt"), replyOn("## Points\n\n- \"Rename Foo\" done."))

	job := f.drive()
	if job.State != revise.Watching {
		t.Fatalf("the job is in %q, want %s", job.State, revise.Watching)
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
