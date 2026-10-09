//go:build unix

package revise_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/github/githubtest"
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
			ft := fileTracker(dir, "")
			start := newTracker()
			start.Say(12, send(1, "/revise Rename Foo."))
			if err := ft.Save(start); err != nil {
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

			got, err := ft.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !intake.Claimed(got.ReactionsOn[1], agent) || got.Writes["React"] != 1 {
				t.Errorf("claimed %v after %d reactions, want claimed by one", intake.Claimed(got.ReactionsOn[1], agent), got.Writes["React"])
			}
			if len(got.PullRequests[12].Labels) != 1 || got.PullRequests[12].Labels[0] != "bug" || got.Writes["Unlabel"] != 1 {
				t.Errorf("labels %v after %d removals, want the hand-off label taken off once", got.PullRequests[12].Labels, got.Writes["Unlabel"])
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
	ft := fileTracker(dir, killAt)

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

// fileTracker is the fixture tracker in a file in dir, so that what a killed
// process did to pull request 12 outlives the process. A non-empty killAt is
// the point this process stops dead at, having told the parent it is there.
// Nothing the claim does not do is served.
func fileTracker(dir, killAt string) *githubtest.File {
	die := func(point string) {
		if killAt != point {
			return
		}
		os.WriteFile(filepath.Join(dir, "ready"), nil, 0o644)
		select {}
	}
	return &githubtest.File{
		Path: filepath.Join(dir, "tracker.json"),
		New: func() *githubtest.Tracker {
			tr := newTracker()
			tr.Fail = func(c githubtest.Call) error {
				switch c.Method {
				case "CheckRuns":
					return errors.New("the claim reads no check runs")
				case "RequiredChecks":
					return errors.New("the claim reads no required checks")
				case "EditComment":
					return errors.New("PATCH comment: this test edits no comment")
				case "EditPullRequest":
					return errors.New("the claim never edits a pull request's description")
				case "Comment":
					return errors.New("a send-back with points is not answered at its claim")
				case "ReactToIssue":
					return errors.New("the revise kind never claims a pull request's description")
				case "Label":
					return errors.New("the revise kind's claim never applies a label")
				}
				return nil
			}
			return tr
		},
		Before: func(c githubtest.Call) {
			switch c.Method {
			case "React":
				die("before-claim")
			case "Unlabel":
				die("before-unlabel")
			}
		},
		After: func(c githubtest.Call) {
			switch c.Method {
			case "React":
				die("after-claim")
			case "Unlabel":
				die("after-unlabel")
			}
		},
	}
}
