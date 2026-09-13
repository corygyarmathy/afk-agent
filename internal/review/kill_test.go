//go:build unix

package review_test

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

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/model"
	"github.com/corygyarmathy/afk-agent/internal/opencode"
	"github.com/corygyarmathy/afk-agent/internal/review"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The environment the parent hands the helper process.
const (
	envKillDir = "AFK_REVIEW_KILL_DIR"
	envKillAt  = "AFK_REVIEW_KILL_AT"
)

// A /review produces one review comment, and killing the process at any point
// in the review and running it again still produces exactly one.
//
// The acceptance criterion of #3, done by actually killing a process rather than
// by simulating one, for the reason the runner's kill test gives: SIGKILL runs
// no defer, releases no lease and flushes nothing, and a simulation only tests
// the paths its author thought of. The tracker is a file, so what the killed
// process did to the pull request survives it the way GitHub would.
//
// The points are the ones where a kill changes what has to happen next: with
// the claim made and nothing else, in the middle of the model run, between the
// commit that moves to verifying and the request that posts, and after the post
// has landed but before the process knows it has.
func TestKillingAReviewAnywhereStillPostsExactlyOne(t *testing.T) {
	for _, at := range []string{"after-claim", "model", "before-post", "after-post"} {
		t.Run(at, func(t *testing.T) {
			dir := t.TempDir()
			ft := &fileTracker{path: filepath.Join(dir, "tracker.json")}
			if err := ft.save(trackerFile{Comments: []github.Comment{command(1)}, Reactions: map[int64][]github.Reaction{}, NextID: 1000}); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(dir, "ready")

			doomed := helper(dir, at)
			doomed.Stdout, doomed.Stderr = os.Stderr, os.Stderr
			if err := doomed.Start(); err != nil {
				t.Fatalf("start the helper: %v", err)
			}
			t.Cleanup(func() { doomed.Process.Kill() })
			waitForFile(t, ready)

			if err := doomed.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatalf("kill the helper: %v", err)
			}
			if err := doomed.Wait(); err == nil {
				t.Fatal("the helper exited cleanly; it was supposed to be killed")
			}

			// A second process, the way a restarted worker or an operator's
			// `afk run` would pick the job up.
			if out, err := helper(dir, "").CombinedOutput(); err != nil {
				t.Fatalf("finishing the review after the kill: %v\n%s", err, out)
			}

			got, err := ft.load()
			if err != nil {
				t.Fatal(err)
			}
			var reviews int
			for _, c := range got.Comments {
				if c.Login == agent && strings.Contains(c.Body, review.Marker(head)) {
					reviews++
				}
			}
			if reviews != 1 {
				t.Errorf("%d reviews on the pull request after a kill at %s, want exactly 1", reviews, at)
			}
			if !intake.Claimed(got.Reactions[1], agent) {
				t.Errorf("the command is not claimed after a kill at %s", at)
			}
		})
	}
}

func helper(dir, killAt string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRunsAReview$")
	cmd.Env = append(os.Environ(), envKillDir+"="+dir, envKillAt+"="+killAt)
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

// TestHelperRunsAReview is not a test. It is the process the kill test kills,
// and then the one that finishes the job.
func TestHelperRunsAReview(t *testing.T) {
	dir := os.Getenv(envKillDir)
	if dir == "" {
		t.Skip("run by the kill test, not directly")
	}
	killAt := os.Getenv(envKillAt)
	ft := &fileTracker{path: filepath.Join(dir, "tracker.json"), killAt: killAt, ready: filepath.Join(dir, "ready")}

	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	d := &review.Deps{
		Tracker: ft,
		Model:   killModel{ft},
		Store:   s,
		Checkout: func(_ context.Context, ws string, _ int) (string, error) {
			return head, os.MkdirAll(filepath.Join(ws, ".git"), 0o755)
		},
		Resolve:  func(context.Context) (model.Candidates, error) { return model.Candidates{first, second}, nil },
		Bound:    2,
		TierWait: time.Hour,
		Login:    agent,
		StateDir: dir,
	}
	reg := transition.MustRegistry(review.Transitions(d)...)

	// The process that finishes runs an hour ahead, past the killed process's
	// lease, rather than waiting it out: reclaiming a dead holder's job after
	// expiry is the store's property and has a test of its own.
	clock := time.Now()
	if killAt == "" {
		clock = clock.Add(time.Hour)
	}
	r := &transition.Runner{Store: s, Registry: reg, Holder: "helper-" + killAt, LeaseTTL: time.Minute, Clock: func() time.Time { return clock }}

	ctx := context.Background()
	job, err := s.Ensure(ctx, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, review.Start, clock)
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if job, err = s.Job(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		next, ok := reg.Next(job.Kind, job.State)
		if !ok {
			t.Fatalf("no transition runs from %q", job.State)
		}
		// An error is a transition that failed and was recorded; the loop
		// reads the state it left and carries on, as the pool would.
		r.Run(ctx, next.Name, job.ID)
		if job, err = s.Job(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		if job.NextRunAt.IsZero() {
			s.Close()
			os.Exit(0)
		}
	}
	t.Fatalf("the review never came to rest; it is in %q", job.State)
}

// fileTracker is one pull request whose comments and reactions live in a file,
// so that what a killed process did to it outlives the process.
type fileTracker struct {
	path   string
	killAt string
	ready  string
}

type trackerFile struct {
	Comments  []github.Comment
	Reactions map[int64][]github.Reaction
	NextID    int64
}

// die stops this process dead at the named point, if it is the one to be
// killed at, having told the parent it is there.
func (ft *fileTracker) die(point string) {
	if ft.killAt != point {
		return
	}
	os.WriteFile(ft.ready, nil, 0o644)
	select {}
}

func (ft *fileTracker) load() (trackerFile, error) {
	b, err := os.ReadFile(ft.path)
	if err != nil {
		return trackerFile{}, err
	}
	var f trackerFile
	err = json.Unmarshal(b, &f)
	if f.Reactions == nil {
		f.Reactions = map[int64][]github.Reaction{}
	}
	return f, err
}

func (ft *fileTracker) save(f trackerFile) error {
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

func (ft *fileTracker) PullRequest(_ context.Context, n int) (github.PullRequest, error) {
	return github.PullRequest{Number: n, State: "open", HeadSHA: head}, nil
}

func (ft *fileTracker) Diff(context.Context, int) (string, error) { return diff, nil }

func (ft *fileTracker) Comments(context.Context, int) ([]github.Comment, error) {
	f, err := ft.load()
	return f.Comments, err
}

func (ft *fileTracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	f, err := ft.load()
	return f.Reactions[id], err
}

func (ft *fileTracker) Comment(_ context.Context, _ int, body string) (github.Comment, error) {
	ft.die("before-post")
	f, err := ft.load()
	if err != nil {
		return github.Comment{}, err
	}
	f.NextID++
	c := github.Comment{ID: f.NextID, Login: agent, Association: "COLLABORATOR", Body: body}
	f.Comments = append(f.Comments, c)
	if err := ft.save(f); err != nil {
		return github.Comment{}, err
	}
	ft.die("after-post")
	return c, nil
}

func (ft *fileTracker) React(_ context.Context, id int64, content string) error {
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

// killModel is a model that answers, unless this is the process to be killed
// in the middle of a model run.
type killModel struct{ ft *fileTracker }

func (m killModel) Run(context.Context, opencode.Request) (opencode.Reply, error) {
	m.ft.die("model")
	return opencode.Reply{Text: "The change is sound.", Cost: 0.01}, nil
}
