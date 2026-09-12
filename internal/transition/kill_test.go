//go:build unix

package transition_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// The environment the parent hands the helper process.
const (
	envStore = "AFK_KILL_TEST_STORE"
	envReady = "AFK_KILL_TEST_READY"
)

// Killing the process mid-transition loses that transition and nothing else.
//
// One of the MVP gates on #2, and the reason it is done by actually killing a
// process rather than by simulating one: what is being tested is that an
// uncatchable signal, arriving at the worst moment, costs one transition. A
// simulation tests the code paths the author thought of, and SIGKILL runs none
// of them - no defer, no release, no flush.
//
// What "nothing else" means, concretely, is the four assertions below: the
// victim keeps its state and its attempt count, no idempotency key was
// reserved for an effect that never happened, the job is reclaimable once the
// dead process's lease lapses and runs normally afterwards, and the job beside
// it in the same store is untouched.
func TestKillingTheProcessMidTransitionLosesOnlyThatTransition(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	readyPath := filepath.Join(dir, "inside-the-transition")

	// Two jobs: the one the doomed process takes, and a bystander.
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	victim := seed(t, s, store.KindReview, 12, "start")
	bystander := seed(t, s, store.KindImplement, 2, "start")
	before, err := s.Job(ctx, bystander.ID)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	// Hand the file over: the child is a separate process, and the assertions
	// afterwards are against what it left on disk.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsATransitionOpen$")
	cmd.Env = append(os.Environ(), envStore+"="+dbPath, envReady+"="+readyPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })

	waitFor(t, readyPath)

	// SIGKILL: no defer runs, no lease is released, nothing is flushed.
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill the helper: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the helper exited cleanly; it was supposed to be killed mid-transition")
	}

	s, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen the store after the kill: %v", err)
	}
	defer s.Close()

	got, err := s.Job(ctx, victim.ID)
	if err != nil {
		t.Fatalf("Job after the kill: %v", err)
	}
	if got.State != "start" {
		t.Errorf("state = %q, want start: the transition did not finish, so it must not have moved the job", got.State)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want 0", got.Attempts)
	}
	if reserved, err := s.Reserved(ctx, helperKey); err != nil || reserved {
		t.Errorf("Reserved(%q) = %v, %v; want false: the effect never happened, so nothing may claim it did", helperKey, reserved, err)
	}

	// The dead process's lease still stands, which is what stops a second
	// worker from picking the job up while the first might still be alive.
	if got.Lease == nil {
		t.Fatal("the killed process's lease is gone; nothing was holding the job")
	}
	if got.Lease.Holder != "the-doomed-process" {
		t.Fatalf("lease holder = %q; the helper never got as far as taking the lease", got.Lease.Holder)
	}
	now := time.Now()
	if _, ok, err := s.Acquire(ctx, victim.ID, "another-worker", now, time.Minute); err != nil || ok {
		t.Errorf("Acquire while the lease stands = %v, %v; want false", ok, err)
	}

	// Once it lapses the job is reclaimable with no operator action, and it
	// runs to completion: the transition was lost, the job was not.
	after := got.Lease.ExpiresAt.Add(time.Second)
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "reviewed"}, nil
		},
	})
	r := runner(s, reg, func(r *transition.Runner) {
		r.Holder = "another-worker"
		r.Clock = func() time.Time { return after }
	})
	if _, err := r.Run(ctx, "review", victim.ID); err != nil {
		t.Fatalf("running the reclaimed job: %v", err)
	}
	if got, _ := s.Job(ctx, victim.ID); got.State != "reviewed" {
		t.Errorf("state = %q, want reviewed", got.State)
	}

	// And the job that had nothing to do with any of it is exactly as it was.
	stillThere, err := s.Job(ctx, bystander.ID)
	if err != nil {
		t.Fatalf("the bystander is gone: %v", err)
	}
	if stillThere.State != before.State || stillThere.Attempts != before.Attempts || stillThere.Lease != nil {
		t.Errorf("bystander = %+v, want %+v untouched and unleased", stillThere, before)
	}
	if !stillThere.NextRunAt.Equal(before.NextRunAt) {
		t.Errorf("bystander NextRunAt = %s, want %s", stillThere.NextRunAt, before.NextRunAt)
	}
}

// helperKey is the idempotency key of the effect the helper's transition would
// have performed, had it ever got that far.
const helperKey = "review-pr-12-killed"

// TestHelperHoldsATransitionOpen is not a test. It is the process the test
// above kills: it takes a real lease on a real job through the real runner, and
// then stops inside the transition, where being killed costs the most.
func TestHelperHoldsATransitionOpen(t *testing.T) {
	dbPath, readyPath := os.Getenv(envStore), os.Getenv(envReady)
	if dbPath == "" {
		t.Skip("helper process; run by TestKillingTheProcessMidTransitionLosesOnlyThatTransition")
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(ctx context.Context, in transition.In) (transition.Result, error) {
			signal(t, readyPath)
			// Mid-transition, and staying there. A sleep rather than a bare
			// channel receive, so the runtime's deadlock detector does not
			// end the process before the test can.
			<-time.After(time.Hour)
			return transition.Result{
				State:   "reviewed",
				Effects: []transition.Effect{{Key: helperKey, Do: func(context.Context) error { return nil }}},
			}, nil
		},
	})

	r := &transition.Runner{
		Store:    s,
		Registry: reg,
		Holder:   "the-doomed-process",
		// Short enough that the test can watch the lease lapse, long enough
		// that it has not lapsed by the time the kill lands.
		LeaseTTL: 30 * time.Second,
	}
	if _, err := r.Run(context.Background(), "review", store.ID(store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12})); err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Fatal("this process was supposed to be killed")
}

// signal tells the parent the transition is under way. Written elsewhere and
// renamed into place, so the parent cannot see a half-written file and kill too
// early.
func signal(t *testing.T, path string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("here"), 0o600); err != nil {
		t.Fatalf("signal: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("signal: %v", err)
	}
}

// waitFor blocks until the helper says it is inside the transition.
func waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the helper never reached the inside of its transition")
}
