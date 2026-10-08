//go:build unix

package revise_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github"
	"github.com/corygyarmathy/afk-agent/internal/intake"
	"github.com/corygyarmathy/afk-agent/internal/revise"
	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The environment the parent hands the helper process.
const (
	envKillDir = "AFK_REVISE_KILL_DIR"
	envKillAt  = "AFK_REVISE_KILL_AT"
)

// A process killed between the claim's commit and its effects, then run again,
// still claims the send-back and takes the hand-off label off, once each.
//
// Done by actually killing a process, for the reason review's kill test gives:
// SIGKILL runs no defer, releases no lease and flushes nothing. The tracker is
// a file, so what the killed process did to the pull request survives it the
// way GitHub would. The points are before the claim lands, after it lands and
// before the label comes off, and after the label comes off and before the
// process knows it has.
func TestKillingAClaimStillClaimsAndUnlabelsOnce(t *testing.T) {
	for _, at := range []string{"before-claim", "after-claim", "before-unlabel", "after-unlabel"} {
		t.Run(at, func(t *testing.T) {
			dir := t.TempDir()
			ft := &fileTracker{path: filepath.Join(dir, "tracker.json")}
			start := trackerFile{
				Comments:  []github.Comment{send(1, "/revise Rename Foo.")},
				Reactions: map[int64][]github.Reaction{},
				Labels:    []string{handOff, "bug"},
				Writes:    map[string]int{},
			}
			if err := ft.save(start); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(dir, "ready")

			doomed := killHelper(dir, at)
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
			if out, err := killHelper(dir, "").CombinedOutput(); err != nil {
				t.Fatalf("finishing the claim after the kill: %v\n%s", err, out)
			}

			got, err := ft.load()
			if err != nil {
				t.Fatal(err)
			}
			if !intake.Claimed(got.Reactions[1], agent) || got.Writes["react"] != 1 {
				t.Errorf("claimed %v after %d reactions, want claimed by one", intake.Claimed(got.Reactions[1], agent), got.Writes["react"])
			}
			if len(got.Labels) != 1 || got.Labels[0] != "bug" || got.Writes["unlabel"] != 1 {
				t.Errorf("labels %v after %d removals, want the hand-off label taken off once", got.Labels, got.Writes["unlabel"])
			}
		})
	}
}

func killHelper(dir, killAt string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperClaimsASendBack$")
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

// TestHelperClaimsASendBack is not a test. It is the process the kill test
// kills, and then the one that finishes the claim.
func TestHelperClaimsASendBack(t *testing.T) {
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
	d := &revise.Deps{Tracker: ft, Store: s, Login: agent, Repo: repo, Rounds: 2, HandOffLabel: handOff, StateDir: dir}
	reg := transition.MustRegistry(revise.Transitions(d)...)

	// The process that finishes runs an hour ahead, past the killed process's
	// lease, rather than waiting it out.
	clock := time.Now()
	if killAt == "" {
		clock = clock.Add(time.Hour)
	}
	r := &transition.Runner{Store: s, Registry: reg, Holder: "helper-" + killAt, LeaseTTL: time.Minute, Clock: func() time.Time { return clock }}

	ctx := context.Background()
	job, err := s.Ensure(ctx, store.KindRevise, store.Subject{Type: store.SubjectPR, Number: 12}, revise.Start, clock)
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if job, err = s.Job(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		next, ok := reg.Next(job.Kind, job.State)
		if !ok || job.NextRunAt.IsZero() {
			if job.State != revise.Revising {
				t.Fatalf("the claim stopped in %q, want revising", job.State)
			}
			s.Close()
			os.Exit(0)
		}
		// An error is a transition that failed and was recorded; the loop
		// reads the state it left and carries on, as the pool would.
		r.Run(ctx, next.Name, job.ID)
	}
	t.Fatalf("the claim never finished; it is in %q", job.State)
}

// fileTracker is pull request 12, whose comments, reactions and labels live in
// a file, so that what a killed process did to it outlives the process.
type fileTracker struct {
	path   string
	killAt string
	ready  string
}

type trackerFile struct {
	Comments  []github.Comment
	Reactions map[int64][]github.Reaction
	Labels    []string

	// Writes counts the writes that landed, by what they were.
	Writes map[string]int
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
	if f.Writes == nil {
		f.Writes = map[string]int{}
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

func (ft *fileTracker) CheckRuns(context.Context, string) ([]github.CheckRun, error) {
	return nil, errors.New("the claim reads no check runs")
}

func (ft *fileTracker) RequiredChecks(context.Context, string) ([]string, error) {
	return nil, errors.New("the claim reads no required checks")
}

func (ft *fileTracker) PullRequest(_ context.Context, n int) (github.PullRequest, error) {
	f, err := ft.load()
	return github.PullRequest{Number: n, State: "open", HeadSHA: head, HeadRef: "feature", HeadRepo: repo, Labels: f.Labels}, err
}

func (ft *fileTracker) EditComment(context.Context, int64, string) error {
	return errors.New("PATCH comment: this test edits no comment")
}

func (ft *fileTracker) EditPullRequest(context.Context, int, string) error {
	return errors.New("the claim never edits a pull request's description")
}

func (ft *fileTracker) Issue(_ context.Context, n int) (github.Issue, error) {
	f, err := ft.load()
	return github.Issue{Number: n, State: "open", PullRequest: true, Labels: f.Labels}, err
}

func (ft *fileTracker) Comments(context.Context, int) ([]github.Comment, error) {
	f, err := ft.load()
	return f.Comments, err
}

func (ft *fileTracker) Reactions(_ context.Context, id int64) ([]github.Reaction, error) {
	f, err := ft.load()
	return f.Reactions[id], err
}

func (ft *fileTracker) IssueReactions(context.Context, int) ([]github.Reaction, error) {
	return nil, nil
}

func (ft *fileTracker) React(_ context.Context, id int64, content string) error {
	ft.die("before-claim")
	f, err := ft.load()
	if err != nil {
		return err
	}
	if !intake.Claimed(f.Reactions[id], agent) {
		f.Reactions[id] = append(f.Reactions[id], github.Reaction{Login: agent, Content: content})
	}
	f.Writes["react"]++
	if err := ft.save(f); err != nil {
		return err
	}
	ft.die("after-claim")
	return nil
}

func (ft *fileTracker) Unlabel(_ context.Context, _ int, label string) error {
	ft.die("before-unlabel")
	f, err := ft.load()
	if err != nil {
		return err
	}
	var kept []string
	for _, l := range f.Labels {
		if l != label {
			kept = append(kept, l)
		}
	}
	f.Labels = kept
	f.Writes["unlabel"]++
	if err := ft.save(f); err != nil {
		return err
	}
	ft.die("after-unlabel")
	return nil
}

func (ft *fileTracker) Comment(context.Context, int, string) (github.Comment, error) {
	return github.Comment{}, errors.New("a send-back with points is not answered at its claim")
}

func (ft *fileTracker) PullRequestReviews(context.Context, int) ([]github.PullRequestReview, error) {
	return nil, nil
}

func (ft *fileTracker) LineComments(context.Context, int, int64) ([]github.LineComment, error) {
	return nil, nil
}

func (ft *fileTracker) PullRequestReviewReactions(context.Context, string) ([]github.Reaction, error) {
	return nil, nil
}

func (ft *fileTracker) ReactToPullRequestReview(context.Context, string, string) error { return nil }

func (ft *fileTracker) ReactToIssue(context.Context, int, string) error {
	return errors.New("the revise kind never claims a pull request's description")
}

func (ft *fileTracker) Label(context.Context, int, string) error {
	return errors.New("the revise kind's claim never applies a label")
}
