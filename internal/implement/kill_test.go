//go:build unix

package implement_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/git"
	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/implement"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The environment the parent hands the helper process.
const (
	envKillDir    = "AFK_IMPLEMENT_KILL_DIR"
	envKillAt     = "AFK_IMPLEMENT_KILL_AT"
	envKillRemote = "AFK_IMPLEMENT_KILL_REMOTE"
	envKillGate   = "AFK_IMPLEMENT_KILL_GATE"
)

// An /implement produces one push per commit, one pull request, and one of each
// comment and label, and killing the process at any point in the kind and
// running it again still does.
//
// The acceptance criterion of #40, done by actually killing a process, for the
// reason review's kill test gives: SIGKILL runs no defer, releases no lease and
// flushes nothing. The tracker is a file, and the remote a bare repository on
// disk, so what the killed process did survives it the way it would on GitHub.
// Pushes are counted from the remote's reflog, where a push that moves nothing
// leaves no entry: pushing the same commit twice is one push.
func TestKillingAnImplementAnywhereStillProducesOneOfEach(t *testing.T) {
	for _, at := range []string{
		"before-claim", "after-claim", "model", "after-model", "before-push", "after-push",
		"before-pr", "after-pr", "watch", "ask-review", "re-ask-review", "before-hand-off", "after-hand-off",
	} {
		t.Run(at, func(t *testing.T) {
			dir := t.TempDir()
			remote := bareRemote(t)
			if _, err := run(remote, "git", "config", "core.logAllRefUpdates", "always"); err != nil {
				t.Fatal(err)
			}
			ft := &killTracker{path: filepath.Join(dir, "tracker.json")}
			if err := ft.save(killFile{Comments: []github.Comment{command(1)}, Reactions: map[int64][]github.Reaction{}, NextID: 1000}); err != nil {
				t.Fatal(err)
			}

			doomed := implementHelper(dir, remote, at, "")
			doomed.Stdout, doomed.Stderr = os.Stderr, os.Stderr
			if err := doomed.Start(); err != nil {
				t.Fatalf("start the helper: %v", err)
			}
			t.Cleanup(func() { doomed.Process.Kill() })
			waitForFile(t, filepath.Join(dir, "ready"))
			if err := doomed.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatalf("kill the helper: %v", err)
			}
			if err := doomed.Wait(); err == nil {
				t.Fatal("the helper exited cleanly; it was supposed to be killed")
			}

			if out, err := implementHelper(dir, remote, "", "").CombinedOutput(); err != nil {
				t.Fatalf("finishing the work after the kill: %v\n%s", err, out)
			}

			got, err := ft.load()
			if err != nil {
				t.Fatal(err)
			}
			if pushes, _ := run(remote, "git", "reflog", "show", "--format=%H", "refs/heads/afk/7-1"); len(strings.Fields(pushes)) != 1 {
				t.Errorf("%d pushes to afk/7-1 after a kill at %s, want 1:\n%s", len(strings.Fields(pushes)), at, pushes)
			}
			if b, _ := run(remote, "git", "for-each-ref", "--format=%(refname:short)", "refs/heads"); b != "afk/7-1\nmain" {
				t.Errorf("the remote has %q after a kill at %s, want main and afk/7-1", b, at)
			}
			if got.Opened != 1 {
				t.Errorf("%d pull requests opened after a kill at %s, want 1", got.Opened, at)
			}
			var reviews, others int
			for _, c := range got.Comments {
				switch {
				case c.Login != agent:
				case strings.Contains(c.Body, "afk:review"):
					reviews++
				default:
					others++
				}
			}
			if reviews != 1 || others != 0 {
				t.Errorf("%d reviews and %d other comments from the agent after a kill at %s, want 1 and 0", reviews, others, at)
			}
			if strings.Join(got.Labels, ",") != "needs-review" {
				t.Errorf("labels %v after a kill at %s, want the hand-off once", got.Labels, at)
			}
			if n := len(got.Reactions[1]); n != 1 || !intake.Claimed(got.Reactions[1], agent) {
				t.Errorf("the command has %d reactions after a kill at %s, want the one claim", n, at)
			}
		})
	}
}

// Work that is handed back produces one hand-back comment and one hand-back
// label, and killing the process between the commit that decides the hand-back
// and either of them, then running again, still does (#58). The job has come
// to rest by then, so nothing but the read-back would ever look again.
func TestKillingAHandBackStillHandsBackOnce(t *testing.T) {
	for _, at := range []string{"before-hand-back", "before-hand-back-label"} {
		t.Run(at, func(t *testing.T) {
			dir := t.TempDir()
			remote := bareRemote(t)
			ft := &killTracker{path: filepath.Join(dir, "tracker.json")}
			if err := ft.save(killFile{Comments: []github.Comment{command(1)}, Reactions: map[int64][]github.Reaction{}, NextID: 1000}); err != nil {
				t.Fatal(err)
			}

			// A gate nothing passes, so the work is handed back on the issue.
			doomed := implementHelper(dir, remote, at, "false")
			doomed.Stdout, doomed.Stderr = os.Stderr, os.Stderr
			if err := doomed.Start(); err != nil {
				t.Fatalf("start the helper: %v", err)
			}
			t.Cleanup(func() { doomed.Process.Kill() })
			waitForFile(t, filepath.Join(dir, "ready"))
			if err := doomed.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatalf("kill the helper: %v", err)
			}
			if err := doomed.Wait(); err == nil {
				t.Fatal("the helper exited cleanly; it was supposed to be killed")
			}

			if out, err := implementHelper(dir, remote, "", "false").CombinedOutput(); err != nil {
				t.Fatalf("finishing the work after the kill: %v\n%s", err, out)
			}

			got, err := ft.load()
			if err != nil {
				t.Fatal(err)
			}
			var handBacks, others int
			for _, c := range got.Comments {
				switch {
				case c.Login != agent:
				case strings.Contains(c.Body, "afk:hand-back"):
					handBacks++
				default:
					others++
				}
			}
			if handBacks != 1 || others != 0 {
				t.Errorf("%d hand-backs and %d other comments from the agent after a kill at %s, want 1 and 0", handBacks, others, at)
			}
			if l := strings.Join(got.LabelsOn[issue], ","); l != "needs-decision" || len(got.Labels) != 1 {
				t.Errorf("labels %v, and %q on the issue, after a kill at %s, want the hand-back label on the issue once", got.Labels, l, at)
			}
			if got.Opened != 0 {
				t.Errorf("%d pull requests opened for work that failed its gate", got.Opened)
			}
			if n := len(got.Reactions[1]); n != 1 || !intake.Claimed(got.Reactions[1], agent) {
				t.Errorf("the command has %d reactions after a kill at %s, want the one claim", n, at)
			}
		})
	}
}

func implementHelper(dir, remote, killAt, gate string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRunsAnImplement$")
	cmd.Env = append(os.Environ(), envKillDir+"="+dir, envKillAt+"="+killAt, envKillRemote+"="+remote, envKillGate+"="+gate)
	return cmd
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the helper never reached the point it was to be killed at")
}

// TestHelperRunsAnImplement is not a test. It is the process the kill test
// kills, and then the one that finishes the job. It stands in for the review
// job too: when the implement job has made that job due, it posts the review
// and puts the job at rest, the way a review job's own transitions would.
func TestHelperRunsAnImplement(t *testing.T) {
	dir := os.Getenv(envKillDir)
	if dir == "" {
		t.Skip("run by the kill test, not directly")
	}
	killAt := os.Getenv(envKillAt)
	remote := os.Getenv(envKillRemote)
	ft := &killTracker{path: filepath.Join(dir, "tracker.json"), killAt: killAt, ready: filepath.Join(dir, "ready"), remote: remote}
	gate := os.Getenv(envKillGate)
	if gate == "" {
		gate = "test -f ok"
	}

	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	d := &implement.Deps{
		Tracker:      ft,
		Model:        killModel{ft},
		Login:        agent,
		BranchPrefix: prefix,
		Remote: git.Remote{URL: remote, Token: func(context.Context) (string, error) {
			// Every read mints a token too. The first one minted once there
			// is a relay is the push's.
			if _, err := os.Stat(filepath.Join(dir, "state", "relays")); err == nil {
				ft.die("before-push")
			}
			return "", nil
		}},
		Resolve:       func(context.Context) (model.Candidates, error) { return model.Candidates{first, second}, nil },
		Bound:         3,
		TierWait:      time.Hour,
		Gate:          gate,
		Attempts:      2,
		HandBackLabel: "needs-decision",
		HandOffLabel:  "needs-review",
		Denylist:      []string{".github/**"},
		CIWait:        time.Minute,
		CICeiling:     48 * time.Hour,
		CIRounds:      1,
		Store:         askStore{s, ft},
		AskReview:     implement.ReviewAsker(intake.Armer{Store: askStore{s, ft}, Holder: "helper-ask-" + killAt, LeaseTTL: time.Minute}),
		StateDir:      filepath.Join(dir, "state"),
	}
	reg := transition.MustRegistry(implement.Transitions(d)...)

	// The process that finishes runs an hour ahead, past the killed process's
	// lease, rather than waiting it out.
	clock := time.Now()
	if killAt == "" {
		clock = clock.Add(time.Hour)
	}
	r := &transition.Runner{Store: s, Registry: reg, Holder: "helper-" + killAt, LeaseTTL: time.Minute, Clock: func() time.Time { return clock }}

	ctx := context.Background()
	job, err := s.Ensure(ctx, store.KindImplement, store.Subject{Type: store.SubjectIssue, Number: issue}, implement.Start, clock)
	if err != nil {
		t.Fatal(err)
	}
	for range 60 {
		if job, err = s.Job(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		if job.NextRunAt.IsZero() {
			s.Close()
			os.Exit(0)
		}
		if job.NextRunAt.After(clock) {
			clock = job.NextRunAt
		}
		standInForTheReview(t, s, ft, clock)
		next, ok := reg.Next(job.Kind, job.State)
		if !ok {
			t.Fatalf("no transition runs from %q", job.State)
		}
		// An error is a transition that failed and was recorded; the loop
		// reads the state it left and carries on, as the pool would.
		r.Run(ctx, next.Name, job.ID)
	}
	t.Fatalf("the work never came to rest; it is in %q", job.State)
}

// standInForTheReview does a due review job's work: one review of the pull
// request's head, and the job at rest.
func standInForTheReview(t *testing.T, s store.Store, ft *killTracker, now time.Time) {
	ctx := context.Background()
	rj, err := s.Job(ctx, "review-pr-101")
	if err != nil || rj.NextRunAt.IsZero() {
		return
	}
	f, err := ft.load()
	if err != nil {
		t.Fatal(err)
	}
	head, err := run(ft.remote, "git", "rev-parse", "refs/heads/afk/7-1")
	if err != nil {
		t.Fatal(err)
	}
	marker := review.Marker(head)
	posted := false
	for _, c := range f.Comments {
		posted = posted || strings.Contains(c.Body, marker)
	}
	// A review that comes to nothing, so the implement job asks again.
	if ft.killAt == "re-ask-review" {
		posted = true
	}
	if !posted {
		f.NextID++
		f.Comments = append(f.Comments, github.Comment{ID: f.NextID, Login: agent, Body: marker + "\nLooks sound."})
		if err := ft.save(f); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := s.Acquire(ctx, rj.ID, "helper-review", now, time.Minute); err != nil || !ok {
		t.Fatalf("taking the review job: %v, %v", ok, err)
	}
	if err := s.Commit(ctx, store.Commit{JobID: rj.ID, Holder: "helper-review", State: review.Start, Release: true}); err != nil {
		t.Fatal(err)
	}
}

// askStore is the store the implement job asks for its review through. It
// dies once the first ask has made the review job, or with a later ask's lease
// on the review job taken and nothing committed: the one store write an effect
// makes, cut in half.
type askStore struct {
	store.Store
	ft *killTracker
}

func (a askStore) Ensure(ctx context.Context, kind store.Kind, subject store.Subject, state string, runAt time.Time) (store.Job, error) {
	job, err := a.Store.Ensure(ctx, kind, subject, state, runAt)
	if err == nil && kind == store.KindReview {
		a.ft.die("ask-review")
	}
	return job, err
}

func (a askStore) Acquire(ctx context.Context, id, holder string, now time.Time, ttl time.Duration) (store.Job, bool, error) {
	job, ok, err := a.Store.Acquire(ctx, id, holder, now, ttl)
	if ok && id == "review-pr-101" {
		a.ft.die("re-ask-review")
	}
	return job, ok, err
}

// killTracker is one issue and the pull requests from it, in a file, so that
// what a killed process did to them outlives the process.
type killTracker struct {
	path, killAt, ready, remote string
}

type killFile struct {
	Comments  []github.Comment
	Reactions map[int64][]github.Reaction
	PRs       []github.PullRequest
	Opened    int
	Labels    []string
	LabelsOn  map[int][]string
	NextID    int64
}

// die stops this process dead at the named point, if it is the one to be
// killed at, having told the parent it is there.
func (ft *killTracker) die(point string) {
	if ft.killAt != point {
		return
	}
	os.WriteFile(ft.ready, nil, 0o644)
	select {}
}

func (ft *killTracker) load() (killFile, error) {
	b, err := os.ReadFile(ft.path)
	if err != nil {
		return killFile{}, err
	}
	var f killFile
	err = json.Unmarshal(b, &f)
	if f.Reactions == nil {
		f.Reactions = map[int64][]github.Reaction{}
	}
	return f, err
}

func (ft *killTracker) save(f killFile) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := ft.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ft.path)
}

func (ft *killTracker) Issue(_ context.Context, n int) (github.Issue, error) {
	f, err := ft.load()
	return github.Issue{Number: n, State: "open", Title: "Reserve a job", Labels: f.LabelsOn[n]}, err
}

func (ft *killTracker) OpenPullRequests(context.Context) ([]github.PullRequest, error) {
	f, err := ft.load()
	if err != nil {
		return nil, err
	}
	// Once the push has landed and before anything opens a pull request:
	// the process knows nothing of the push yet.
	if len(f.PRs) == 0 {
		if at, _ := run(ft.remote, "git", "rev-parse", "--verify", "--quiet", "refs/heads/afk/7-1"); at != "" {
			ft.die("after-push")
		}
	}
	return f.PRs, nil
}

func (ft *killTracker) Comments(context.Context, int) ([]github.Comment, error) {
	f, err := ft.load()
	return f.Comments, err
}

func (ft *killTracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	f, err := ft.load()
	return f.Reactions[id], err
}

func (ft *killTracker) Comment(_ context.Context, _ int, body string) (github.Comment, error) {
	if strings.Contains(body, "afk:hand-back") {
		ft.die("before-hand-back")
	}
	f, err := ft.load()
	if err != nil {
		return github.Comment{}, err
	}
	f.NextID++
	c := github.Comment{ID: f.NextID, Login: agent, Body: body}
	f.Comments = append(f.Comments, c)
	return c, ft.save(f)
}

func (ft *killTracker) React(_ context.Context, id int64, content string) error {
	ft.die("before-claim")
	f, err := ft.load()
	if err != nil {
		return err
	}
	if !intake.Claimed(f.Reactions[id], agent) {
		f.Reactions[id] = append(f.Reactions[id], github.Reaction{Login: agent, Content: content})
	}
	if err := ft.save(f); err != nil {
		return err
	}
	ft.die("after-claim")
	return nil
}

func (ft *killTracker) Label(_ context.Context, n int, label string) error {
	if label == "needs-decision" {
		ft.die("before-hand-back-label")
	} else {
		ft.die("before-hand-off")
	}
	f, err := ft.load()
	if err != nil {
		return err
	}
	f.Labels = append(f.Labels, label)
	if f.LabelsOn == nil {
		f.LabelsOn = map[int][]string{}
	}
	f.LabelsOn[n] = append(f.LabelsOn[n], label)
	for i := range f.PRs {
		f.PRs[i].Labels = append(f.PRs[i].Labels, label)
	}
	if err := ft.save(f); err != nil {
		return err
	}
	ft.die("after-hand-off")
	return nil
}

func (ft *killTracker) IssueReactions(context.Context, int) ([]github.Reaction, error) {
	return nil, nil
}

func (ft *killTracker) ReactToIssue(context.Context, int, string) error { return nil }

func (ft *killTracker) CreatePullRequest(_ context.Context, req github.NewPullRequest) (github.PullRequest, error) {
	ft.die("before-pr")
	f, err := ft.load()
	if err != nil {
		return github.PullRequest{}, err
	}
	f.Opened++
	pr := github.PullRequest{Number: 100 + f.Opened, State: "open", HeadRef: req.Head, Login: agent, Title: req.Title, Body: req.Body}
	f.PRs = append(f.PRs, pr)
	if err := ft.save(f); err != nil {
		return github.PullRequest{}, err
	}
	ft.die("after-pr")
	return pr, nil
}

func (ft *killTracker) CheckRuns(context.Context, string) ([]github.CheckRun, error) {
	ft.die("watch")
	return green("", 0), nil
}

// killModel is a model that commits the work, unless this is the process to be
// killed during the run.
type killModel struct{ ft *killTracker }

func (m killModel) Run(_ context.Context, req opencode.Request) (opencode.Reply, error) {
	m.ft.die("model")
	// A run after a kill finds its own work already done, as a real
	// session reading the workspace would.
	if _, err := os.Stat(filepath.Join(req.Dir, "ok")); err != nil {
		if err := commit("ok")(req.Dir); err != nil {
			return opencode.Reply{}, err
		}
	}
	m.ft.die("after-model")
	return opencode.Reply{Text: "Added ok.", Session: "ses_kill"}, nil
}
