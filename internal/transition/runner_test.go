package transition_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/store/storetest"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

func openStore(t *testing.T) store.Store {
	return storetest.Open(t)
}

// seed puts a job in the store in a given state, which is the "fixture state" a
// transition is tested from.
func seed(t *testing.T, s store.Store, kind store.Kind, num int, state string) store.Job {
	return storetest.Seed(t, s, kind, num, state)
}

func runner(s store.Store, reg *transition.Registry, mut ...func(*transition.Runner)) *transition.Runner {
	r := &transition.Runner{
		Store:    s,
		Registry: reg,
		Holder:   "test",
		LeaseTTL: time.Minute,
	}
	for _, m := range mut {
		m(r)
	}
	return r
}

// The ordering the whole design rests on: the state change and the key are
// committed first, and the outward effect happens after. A process killed
// between them loses the effect, which the next transition can see is missing;
// the other ordering loses the key, and posts the comment twice.
func TestTheStateIsCommittedBeforeTheEffectHappens(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	var stateWhenEffectRan string
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{
				State: "reviewed",
				Effects: []transition.Effect{{
					Key: "review-pr-12-abc123",
					Do: func(ctx context.Context) error {
						j, err := s.Job(ctx, in.Job.ID)
						if err != nil {
							return err
						}
						stateWhenEffectRan = j.State
						return nil
					},
				}},
			}, nil
		},
	})

	out, err := runner(s, reg).Run(ctx, "review", job.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stateWhenEffectRan != "reviewed" {
		t.Errorf("state seen by the effect = %q; want reviewed - the effect ran before the commit", stateWhenEffectRan)
	}
	if len(out.Performed) != 1 || out.Performed[0] != "review-pr-12-abc123" {
		t.Errorf("Performed = %v, want the one key", out.Performed)
	}

	reserved, err := s.Reserved(ctx, "review-pr-12-abc123")
	if err != nil || !reserved {
		t.Errorf("Reserved = %v, %v; want true, nil", reserved, err)
	}
}

// Replaying a transition that has already had its effect must not have it
// again. This is the guarantee that makes crash-anywhere-and-resume safe
// (ADR 0001 §5).
func TestAReplayDoesNotPerformAnEffectTwice(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	posts := 0
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{
				State: "reviewed",
				Effects: []transition.Effect{{
					// The key describes the effect, not the attempt: the same
					// review of the same head is the same key.
					Key: "review-pr-12-abc123",
					Do:  func(context.Context) error { posts++; return nil },
				}},
			}, nil
		},
	})
	r := runner(s, reg)

	if _, err := r.Run(ctx, "review", job.ID); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// The subject comes round again - a re-derived queue, an operator running
	// it by hand - and the job is back in the state the transition runs from.
	if _, ok, err := s.Acquire(ctx, job.ID, "test", time.Now(), time.Minute); err != nil || !ok {
		t.Fatalf("Acquire: %v, %v", ok, err)
	}
	if err := s.Commit(ctx, store.Commit{JobID: job.ID, Holder: "test", State: "start", Release: true}); err != nil {
		t.Fatalf("reset state: %v", err)
	}

	out, err := r.Run(ctx, "review", job.ID)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if posts != 1 {
		t.Errorf("the effect happened %d times; want exactly 1", posts)
	}
	if len(out.Skipped) != 1 {
		t.Errorf("Skipped = %v, want the already-reserved key", out.Skipped)
	}
	if len(out.Performed) != 0 {
		t.Errorf("Performed = %v, want none", out.Performed)
	}
}

func TestAFailedTransitionPerformsNoEffectAndReservesNoKey(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	posted := false
	boom := errors.New("the model refused")
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{
				State:   "reviewed",
				Effects: []transition.Effect{{Key: "k", Do: func(context.Context) error { posted = true; return nil }}},
			}, boom
		},
	})

	_, err := runner(s, reg).Run(ctx, "review", job.ID)
	if !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want %v", err, boom)
	}
	if posted {
		t.Error("the effect happened despite the transition failing")
	}
	if reserved, _ := s.Reserved(ctx, "k"); reserved {
		t.Error("a key was reserved for an effect that never happened")
	}

	got, err := s.Job(ctx, job.ID)
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if got.State != "start" {
		t.Errorf("state = %q; want start - a failure does not move the job", got.State)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
	if got.Lease != nil {
		t.Errorf("lease = %+v; want it released", got.Lease)
	}
}

func TestAFailureParksWithoutABackoffAndSchedulesWithOne(t *testing.T) {
	boom := errors.New("nope")
	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{}, boom
		},
	})

	t.Run("no policy parks the job", func(t *testing.T) {
		ctx := context.Background()
		s := openStore(t)
		job := seed(t, s, store.KindReview, 12, "start")

		out, err := runner(s, reg).Run(ctx, "review", job.ID)
		if !errors.Is(err, boom) {
			t.Fatalf("Run error = %v, want %v", err, boom)
		}
		if !out.Parked {
			t.Error("Parked = false; want true")
		}
		got, _ := s.Job(ctx, job.ID)
		if !got.NextRunAt.IsZero() {
			t.Errorf("NextRunAt = %s; want nothing scheduled, or the job is due now and fails again now", got.NextRunAt)
		}
	})

	t.Run("a policy schedules re-entry", func(t *testing.T) {
		ctx := context.Background()
		s := openStore(t)
		job := seed(t, s, store.KindReview, 12, "start")
		at := time.Now().Add(time.Hour).Round(time.Second)

		r := runner(s, reg, func(r *transition.Runner) {
			r.Backoff = func(attempts int) (time.Time, bool) {
				if attempts > 3 {
					return time.Time{}, false
				}
				return at, true
			}
		})
		if _, err := r.Run(ctx, "review", job.ID); !errors.Is(err, boom) {
			t.Fatalf("Run error = %v, want %v", err, boom)
		}
		got, _ := s.Job(ctx, job.ID)
		if !got.NextRunAt.Equal(at.UTC()) {
			t.Errorf("NextRunAt = %s, want %s", got.NextRunAt, at.UTC())
		}
	})
}

// A panicking transition costs its own job and nothing else: the process
// survives, the error names it, and the lease goes back rather than being
// waited out by the next worker.
func TestAPanicCostsTheJobAndReleasesTheLease(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	reg := transition.MustRegistry(transition.Transition{
		Name: "review", Kind: store.KindReview, From: "start",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			panic("nil map write, at 04:00, unattended")
		},
	})

	_, err := runner(s, reg).Run(ctx, "review", job.ID)
	if err == nil {
		t.Fatal("Run = nil error; want the panic reported")
	}
	got, _ := s.Job(ctx, job.ID)
	if got.Lease != nil {
		t.Errorf("lease = %+v; want it released", got.Lease)
	}
	if _, ok, aerr := s.Acquire(ctx, job.ID, "someone-else", time.Now(), time.Minute); aerr != nil || !ok {
		t.Errorf("Acquire after a panic = %v, %v; want another worker to be able to take it", ok, aerr)
	}
}

// An attempt is a run that returned an error. Errors carry over while the job
// stays where it is, a stay in between neither adds to the count nor resets
// it, and a move starts it again (#74).
func TestAttemptsCountErrorsSinceTheLastMove(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindImplement, 2, "ci")

	var fail bool
	wait := transition.MustRegistry(transition.Transition{
		Name: "watch-ci", Kind: store.KindImplement, From: "ci",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			if fail {
				return transition.Result{}, errors.New("502 Bad Gateway")
			}
			return transition.Result{State: "ci", RunAt: in.Now.Add(time.Minute)}, nil
		},
	})
	r := runner(s, wait)
	r.Backoff = func(int) (time.Time, bool) { return time.Now(), true }

	for i, step := range []struct {
		fail     bool
		attempts int
	}{
		{false, 0},
		{true, 1},
		{false, 1},
		{false, 1},
		{true, 2},
	} {
		fail = step.fail
		if _, err := r.Run(ctx, "watch-ci", job.ID); (err != nil) != step.fail {
			t.Fatalf("run %d: error = %v, want one: %v", i+1, err, step.fail)
		}
		got, _ := s.Job(ctx, job.ID)
		if got.Attempts != step.attempts {
			t.Fatalf("after run %d attempts = %d, want %d", i+1, got.Attempts, step.attempts)
		}
	}

	// Moving on means the job got somewhere, and the count starts again.
	advance := transition.MustRegistry(transition.Transition{
		Name: "hand-off", Kind: store.KindImplement, From: "ci",
		Run: func(context.Context, transition.In) (transition.Result, error) {
			return transition.Result{State: "handed-off"}, nil
		},
	})
	if _, err := runner(s, advance).Run(ctx, "hand-off", job.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := s.Job(ctx, job.ID)
	if got.State != "handed-off" || got.Attempts != 0 {
		t.Errorf("state = %q, attempts = %d; want handed-off, 0", got.State, got.Attempts)
	}
}

// A job that waited by staying and then failed once backs off as if it had
// failed once, and an error that keeps coming back still runs out of retries
// and parks: waiting does not spend the bound, and does not refill it (#74).
func TestAStayDoesNotSpendTheRetriesAnErrorNeeds(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindImplement, 2, "ci")

	var fail bool
	reg := transition.MustRegistry(transition.Transition{
		Name: "watch-ci", Kind: store.KindImplement, From: "ci",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			if fail {
				return transition.Result{}, errors.New("502 Bad Gateway")
			}
			return transition.Result{State: "ci", RunAt: in.Now.Add(time.Minute)}, nil
		},
	})
	const maxAttempts = 3
	var backoffs []int
	r := runner(s, reg)
	r.Backoff = func(attempts int) (time.Time, bool) {
		backoffs = append(backoffs, attempts)
		return time.Now(), attempts < maxAttempts
	}

	for i := 0; i < 10; i++ {
		if _, err := r.Run(ctx, "watch-ci", job.ID); err != nil {
			t.Fatalf("stay %d: %v", i+1, err)
		}
	}
	fail = true
	out, err := r.Run(ctx, "watch-ci", job.ID)
	if err == nil {
		t.Fatal("Run after ten stays: no error, want the 502")
	}
	if len(backoffs) != 1 || backoffs[0] != 1 || out.Parked {
		t.Fatalf("after ten stays and one error: Backoff saw %v, parked %v; want [1], not parked", backoffs, out.Parked)
	}

	// Stays between the errors do not reset the count either.
	for attempt := 2; attempt <= maxAttempts; attempt++ {
		fail = false
		if _, err := r.Run(ctx, "watch-ci", job.ID); err != nil {
			t.Fatalf("stay before error %d: %v", attempt, err)
		}
		fail = true
		out, err = r.Run(ctx, "watch-ci", job.ID)
		if err == nil {
			t.Fatalf("error %d: Run returned none", attempt)
		}
	}
	got, _ := s.Job(ctx, job.ID)
	if !out.Parked || !got.NextRunAt.IsZero() || got.Attempts != maxAttempts {
		t.Errorf("after %d errors: parked %v, due %v, attempts %d; want parked, unscheduled, %d",
			maxAttempts, out.Parked, got.NextRunAt, got.Attempts, maxAttempts)
	}
}

// A run that decided to stay is a stay and not an attempt; a run that failed
// is an attempt and not a stay. The candidate model is chosen by the stays, so
// this is what keeps an error that is not the model's from moving a job on to
// the next candidate (#62).
func TestStaysCountOnlyRunsThatDecidedToStay(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 2, "reviewing")

	var fail bool
	reg := transition.MustRegistry(transition.Transition{
		Name: "review-run", Kind: store.KindReview, From: "reviewing",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			if fail {
				return transition.Result{}, errors.New("502 Bad Gateway")
			}
			if in.Job.Stays == 2 {
				return transition.Result{State: "posting", RunAt: in.Now}, nil
			}
			return transition.Result{State: "reviewing", RunAt: in.Now}, nil
		},
	})
	r := runner(s, reg)
	r.Backoff = func(int) (time.Time, bool) { return time.Now(), true }

	for i, step := range []struct {
		fail            bool
		state           string
		attempts, stays int
	}{
		{false, "reviewing", 0, 1},
		{true, "reviewing", 1, 1},
		{true, "reviewing", 2, 1},
		{false, "reviewing", 2, 2},
		{false, "posting", 0, 0},
	} {
		fail = step.fail
		if _, err := r.Run(ctx, "review-run", job.ID); (err != nil) != step.fail {
			t.Fatalf("run %d: error = %v, want one: %v", i+1, err, step.fail)
		}
		got, _ := s.Job(ctx, job.ID)
		if got.State != step.state || got.Attempts != step.attempts || got.Stays != step.stays {
			t.Fatalf("after run %d: %s, attempts %d, stays %d; want %s, %d, %d",
				i+1, got.State, got.Attempts, got.Stays, step.state, step.attempts, step.stays)
		}
	}
}

// Cancellation must not reach the commit or the release. A SIGTERM that did
// would throw away a transition that had already finished, and leave a lease
// standing on a process that had exited, so the next worker waits out a full
// TTL for a job nobody is working on. The transition's own work stays
// cancellable - losing it is allowed; the writes that record what happened to
// the job are not.
func TestCancellationDoesNotReachTheCommitOrTheRelease(t *testing.T) {
	var cancel context.CancelFunc
	boom := errors.New("gone")
	// The stop lands mid-transition, where a cancellation-aware commit or
	// release would be lost to it. A non-nil failure makes the run take the
	// failure path instead of the success path.
	reg := func(fail error) *transition.Registry {
		return transition.MustRegistry(transition.Transition{
			Name: "review", Kind: store.KindReview, From: "start",
			Run: func(ctx context.Context, _ transition.In) (transition.Result, error) {
				cancel()
				if fail != nil {
					return transition.Result{}, fail
				}
				return transition.Result{
					State:   "reviewed",
					Effects: []transition.Effect{{Key: "k", Do: func(context.Context) error { return nil }}},
				}, nil
			},
		})
	}

	t.Run("the commit lands", func(t *testing.T) {
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		s := openStore(t)
		job := seed(t, s, store.KindReview, 12, "start")

		out, err := runner(s, reg(nil)).Run(ctx, "review", job.ID)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if out.From != "start" || out.To != "reviewed" {
			t.Errorf("outcome = %s -> %s, want start -> reviewed", out.From, out.To)
		}
		got, _ := s.Job(context.Background(), job.ID)
		if got.State != "reviewed" {
			t.Errorf("state = %q, want reviewed: a finished transition must not be thrown away", got.State)
		}
		if got.Lease != nil {
			t.Errorf("lease = %+v; want it released", got.Lease)
		}
		if reserved, _ := s.Reserved(context.Background(), "k"); !reserved {
			t.Error("the key was not reserved with the commit")
		}
	})

	t.Run("the release lands", func(t *testing.T) {
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		s := openStore(t)
		job := seed(t, s, store.KindReview, 12, "start")

		if _, err := runner(s, reg(boom)).Run(ctx, "review", job.ID); !errors.Is(err, boom) {
			t.Fatalf("Run error = %v, want %v", err, boom)
		}
		got, _ := s.Job(context.Background(), job.ID)
		if got.Lease != nil {
			t.Errorf("lease = %+v; want it released: the next worker must not wait out a TTL for a process that has exited", got.Lease)
		}
	})

	t.Run("the transition's own work stays cancellable", func(t *testing.T) {
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		s := openStore(t)
		job := seed(t, s, store.KindReview, 12, "start")

		waitReg := transition.MustRegistry(transition.Transition{
			Name: "review", Kind: store.KindReview, From: "start",
			Run: func(ctx context.Context, _ transition.In) (transition.Result, error) {
				cancel()
				<-ctx.Done()
				return transition.Result{State: "reviewed"}, ctx.Err()
			},
		})
		_, err := runner(s, waitReg).Run(ctx, "review", job.ID)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want %v: losing the work is allowed, and here it is what happened", err, context.Canceled)
		}
		got, _ := s.Job(context.Background(), job.ID)
		if got.State != "start" {
			t.Errorf("state = %q, want start: the transition was lost, and lost is what it must be", got.State)
		}
	})
}

func TestRunRefusesWhatItCannotRun(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")
	reg := transition.MustRegistry(fixture("review"))
	r := runner(s, reg)

	t.Run("an unknown transition", func(t *testing.T) {
		if _, err := r.Run(ctx, "frobnicate", job.ID); !errors.Is(err, transition.ErrUnknownTransition) {
			t.Errorf("Run error = %v, want ErrUnknownTransition", err)
		}
	})

	t.Run("a job that is not there", func(t *testing.T) {
		if _, err := r.Run(ctx, "review", "review-pr-999"); !errors.Is(err, store.ErrNoJob) {
			t.Errorf("Run error = %v, want ErrNoJob", err)
		}
	})

	// A hand-run alongside a running pool must not be able to take a job out
	// from under a live worker. This is the reason `afk run` is safe to use on
	// a host where the daemon is up.
	t.Run("a job a live process holds", func(t *testing.T) {
		if _, ok, err := s.Acquire(ctx, job.ID, "the-daemon", time.Now(), time.Minute); err != nil || !ok {
			t.Fatalf("Acquire: %v, %v", ok, err)
		}
		if _, err := r.Run(ctx, "review", job.ID); !errors.Is(err, transition.ErrHeld) {
			t.Errorf("Run error = %v, want ErrHeld", err)
		}
		if err := s.Release(ctx, job.ID, "the-daemon"); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})

	// A hand-invocation is the same thing as a scheduled one (ADR 0001 §4),
	// so it obeys the same state machine: there is no "run it anyway".
	t.Run("a job in the wrong state", func(t *testing.T) {
		if _, ok, err := s.Acquire(ctx, job.ID, "test", time.Now(), time.Minute); err != nil || !ok {
			t.Fatalf("Acquire: %v, %v", ok, err)
		}
		if err := s.Commit(ctx, store.Commit{JobID: job.ID, Holder: "test", State: "reviewed", Release: true}); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if _, err := r.Run(ctx, "review", job.ID); !errors.Is(err, transition.ErrWrongState) {
			t.Errorf("Run error = %v, want ErrWrongState", err)
		}
	})

	t.Run("a job of another kind", func(t *testing.T) {
		other := seed(t, s, store.KindImplement, 2, "start")
		_, err := r.Run(ctx, "review", other.ID)
		if err == nil {
			t.Fatal("Run = nil error; want a refusal")
		}
	})
}

func TestTheLeaseIsReleasedWhenTheTransitionIsDone(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")
	reg := transition.MustRegistry(fixture("review"))

	if _, err := runner(s, reg).Run(ctx, "review", job.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := s.Job(ctx, job.ID)
	if got.Lease != nil {
		t.Errorf("lease = %+v; want it released - nothing runs between transitions", got.Lease)
	}
}

// A result due now is due the moment it commits, so the lease is what keeps
// the job's next transition from running while this one's effect is still in
// flight - a verify that read GitHub before the post landed would post again.
func TestNoOtherWorkerRunsTheJobWhileAnEffectIsInFlight(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	inside, letGo := make(chan struct{}), make(chan struct{})
	reg := transition.MustRegistry(
		transition.Transition{
			Name: "post", Kind: store.KindReview, From: "start",
			Run: func(_ context.Context, in transition.In) (transition.Result, error) {
				return transition.Result{
					State: "verifying",
					RunAt: in.Now,
					Effects: []transition.Effect{{Key: "review-pr-12-abc123", Do: func(context.Context) error {
						close(inside)
						<-letGo
						return nil
					}}},
				}, nil
			},
		},
		fixture("verify", func(t *transition.Transition) { t.From = "verifying" }),
	)

	first := runner(s, reg, func(r *transition.Runner) { r.Holder = "first" })
	second := runner(s, reg, func(r *transition.Runner) { r.Holder = "second" })

	// Let the effect go on every way out of the test, so a failure below does
	// not leave the first worker's goroutine blocked behind it.
	t.Cleanup(func() {
		select {
		case <-letGo:
		default:
			close(letGo)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := first.Run(ctx, "post", job.ID)
		done <- err
	}()
	select {
	case <-inside:
	case err := <-done:
		t.Fatalf("Run returned before its effect ran: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the effect never started")
	}

	// Committed, due, and still not the second worker's to take.
	got, _ := s.Job(ctx, job.ID)
	if got.State != "verifying" {
		t.Fatalf("state = %q, want verifying: the effect runs after the commit", got.State)
	}
	if _, err := second.Run(ctx, "verify", job.ID); !errors.Is(err, transition.ErrHeld) {
		t.Errorf("Run while the effect is in flight = %v, want %v", err, transition.ErrHeld)
	}
	if j, ok, err := s.Due(ctx, "second", time.Now(), time.Minute); err != nil || ok {
		t.Errorf("Due while the effect is in flight = %s, %v, %v; want nothing", j.ID, ok, err)
	}

	close(letGo)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, _ := s.Job(ctx, job.ID); got.Lease != nil {
		t.Errorf("lease = %+v; want it released once the effect is done", got.Lease)
	}
	if _, err := second.Run(ctx, "verify", job.ID); err != nil {
		t.Errorf("Run once the effect is done: %v", err)
	}
}

// An effect that fails has still finished, and the job is not held past it.
func TestTheLeaseIsReleasedWhenAnEffectFails(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")
	boom := errors.New("tracker down")
	reg := transition.MustRegistry(transition.Transition{
		Name: "post", Kind: store.KindReview, From: "start",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{
				State:   "verifying",
				RunAt:   in.Now,
				Effects: []transition.Effect{{Key: "k", Do: func(context.Context) error { return boom }}},
			}, nil
		},
	})

	if _, err := runner(s, reg).Run(ctx, "post", job.ID); !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want %v", err, boom)
	}
	got, _ := s.Job(ctx, job.ID)
	if got.State != "verifying" || got.Lease != nil {
		t.Errorf("job = %q, lease %+v; want verifying and released", got.State, got.Lease)
	}
}

// The lease is renewed at the commit, so the effects are held for a whole
// lease however long the transition took: a push that starts at the end of a
// slow transition is not left with the few seconds it had left.
func TestTheEffectsAreHeldForAWholeLeaseFromTheCommit(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	// The transition takes most of the lease: the runner reads the clock once
	// to take the lease and again to commit.
	start := time.Now().Truncate(time.Nanosecond)
	late := start.Add(50 * time.Second)
	clock := []time.Time{start, late}

	var during *store.Lease
	reg := transition.MustRegistry(transition.Transition{
		Name: "post", Kind: store.KindReview, From: "start",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{
				State: "verifying",
				RunAt: in.Now,
				Effects: []transition.Effect{{Key: "k", Do: func(ctx context.Context) error {
					j, err := s.Job(ctx, job.ID)
					during = j.Lease
					return err
				}}},
			}, nil
		},
	})
	r := runner(s, reg, func(r *transition.Runner) {
		r.Clock = func() time.Time {
			now := clock[0]
			if len(clock) > 1 {
				clock = clock[1:]
			}
			return now
		}
	})

	if _, err := r.Run(ctx, "post", job.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := late.Add(r.LeaseTTL); during == nil || !during.ExpiresAt.Equal(want) {
		t.Errorf("lease during the effect = %+v; want it to run to %v, a whole lease from the commit", during, want)
	}
}

// run is Run on its own goroutine, failing the test rather than hanging it if
// the run never returns - which is what a run whose work outlived its lease
// used to do.
func run(t *testing.T, r *transition.Runner, name, jobID string) (transition.Outcome, error) {
	t.Helper()
	type result struct {
		out transition.Outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := r.Run(context.Background(), name, jobID)
		done <- result{out, err}
	}()
	select {
	case res := <-done:
		return res.out, res.err
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return: the work outlived its lease")
		return transition.Outcome{}, nil
	}
}

// An effect that hangs must not outlive the lease it runs under. Past it,
// another worker can take the job while the effect is still in flight, which
// is #64's race come back later, and this worker is held behind it. Cancelled,
// the run returns and the job goes back on the queue on time.
func TestAnEffectStillRunningWhenTheLeaseRunsOutIsCancelled(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	reg := transition.MustRegistry(transition.Transition{
		Name: "post", Kind: store.KindReview, From: "start",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{
				State: "verifying",
				RunAt: in.Now,
				Effects: []transition.Effect{{Key: "review-pr-12-abc123", Do: func(ctx context.Context) error {
					<-ctx.Done()
					return ctx.Err()
				}}},
			}, nil
		},
	})
	r := runner(s, reg, func(r *transition.Runner) { r.LeaseTTL = 100 * time.Millisecond })

	out, err := run(t, r, "post", job.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want %v", err, context.DeadlineExceeded)
	}
	if len(out.Performed) != 0 {
		t.Errorf("Performed = %v, want nothing: the effect did not finish", out.Performed)
	}
	got, _ := s.Job(ctx, job.ID)
	if got.State != "verifying" || got.Lease != nil {
		t.Errorf("job = %q, lease %+v; want verifying and released", got.State, got.Lease)
	}
}

// The effects' deadline is the renewed lease's: it runs out no later than the
// lease's expiry, and not much earlier. Earlier would cut short an effect the
// lease still covers; later is the race.
func TestTheEffectsRunOutNoLaterThanTheirLease(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindReview, 12, "start")

	var (
		deadline time.Time
		ok       bool
		lease    *store.Lease
	)
	reg := transition.MustRegistry(transition.Transition{
		Name: "post", Kind: store.KindReview, From: "start",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{
				State: "verifying",
				RunAt: in.Now,
				Effects: []transition.Effect{{Key: "k", Do: func(ctx context.Context) error {
					deadline, ok = ctx.Deadline()
					j, err := s.Job(ctx, job.ID)
					lease = j.Lease
					return err
				}}},
			}, nil
		},
	})
	r := runner(s, reg)

	if _, err := r.Run(ctx, "post", job.ID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ok || lease == nil {
		t.Fatalf("deadline %v (set %v), lease %+v; want both", deadline, ok, lease)
	}
	if deadline.After(lease.ExpiresAt) {
		t.Errorf("deadline %v is after the lease's expiry %v", deadline, lease.ExpiresAt)
	}
	if gap := lease.ExpiresAt.Sub(deadline); gap > time.Second {
		t.Errorf("deadline %v is %v short of the lease's expiry %v", deadline, gap, lease.ExpiresAt)
	}
}
