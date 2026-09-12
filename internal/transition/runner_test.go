package transition_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

func openStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seed puts a job in the store in a given state, which is the "fixture state" a
// transition is tested from.
func seed(t *testing.T, s store.Store, kind store.Kind, num int, state string) store.Job {
	t.Helper()
	typ := store.SubjectPR
	if kind == store.KindImplement {
		typ = store.SubjectIssue
	}
	j, err := s.Ensure(context.Background(), kind, store.Subject{Type: typ, Number: num}, state, time.Now())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return j
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

func TestAttemptsCarryOnARetryAndResetOnAMove(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	job := seed(t, s, store.KindImplement, 2, "ci")

	// Staying in the same state is a retry, and the count carries.
	wait := transition.MustRegistry(transition.Transition{
		Name: "watch-ci", Kind: store.KindImplement, From: "ci",
		Run: func(_ context.Context, in transition.In) (transition.Result, error) {
			return transition.Result{State: "ci", RunAt: in.Now.Add(time.Minute)}, nil
		},
	})
	for i := 1; i <= 3; i++ {
		if _, err := runner(s, wait).Run(ctx, "watch-ci", job.ID); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		got, _ := s.Job(ctx, job.ID)
		if got.Attempts != i {
			t.Fatalf("after %d runs attempts = %d, want %d", i, got.Attempts, i)
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
