package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
)

// open makes a store in a temp directory and closes it when the test ends.
func open(t *testing.T, dir string) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustEnsure(t *testing.T, s store.Store, kind store.Kind, subj store.Subject, state string, runAt time.Time) store.Job {
	t.Helper()
	j, err := s.Ensure(context.Background(), kind, subj, state, runAt)
	if err != nil {
		t.Fatalf("Ensure(%s, %s): %v", kind, subj, err)
	}
	return j
}

// --- Acceptance: a lease is exclusive and expiring. ---

func TestLeaseIsExclusive(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)

	if _, ok, err := s.Acquire(ctx, j.ID, "worker-a", now, time.Minute); err != nil || !ok {
		t.Fatalf("first Acquire = %v, %v; want true, nil", ok, err)
	}
	if _, ok, err := s.Acquire(ctx, j.ID, "worker-b", now, time.Minute); err != nil || ok {
		t.Fatalf("second Acquire = %v, %v; want false, nil", ok, err)
	}
}

func TestLeaseExpiryIsReclaimableWithoutOperatorAction(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)

	// worker-a takes a short lease and then, as far as anything else can
	// tell, dies: it never releases and never commits.
	if _, ok, _ := s.Acquire(ctx, j.ID, "worker-a", now, time.Minute); !ok {
		t.Fatal("worker-a could not acquire")
	}

	later := now.Add(time.Minute + time.Second)
	got, ok, err := s.Acquire(ctx, j.ID, "worker-b", later, time.Minute)
	if err != nil || !ok {
		t.Fatalf("Acquire after expiry = %v, %v; want true, nil", ok, err)
	}
	if got.Lease.Holder != "worker-b" {
		t.Errorf("holder = %q; want worker-b", got.Lease.Holder)
	}
}

func TestHolderRenewsItsOwnLease(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)

	if _, ok, _ := s.Acquire(ctx, j.ID, "worker-a", now, time.Minute); !ok {
		t.Fatal("worker-a could not acquire")
	}
	got, ok, err := s.Acquire(ctx, j.ID, "worker-a", now.Add(time.Second), time.Hour)
	if err != nil || !ok {
		t.Fatalf("renewal = %v, %v; want true, nil", ok, err)
	}
	if want := now.Add(time.Second + time.Hour); !got.Lease.ExpiresAt.Equal(want) {
		t.Errorf("renewed expiry = %v; want %v", got.Lease.ExpiresAt, want)
	}
}

// TestDueIsAtomicUnderConcurrentWorkers is the criterion's "atomic under
// concurrent workers": many workers race for one due job and exactly one may
// come away with it.
func TestDueIsAtomicUnderConcurrentWorkers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir)
	now := time.Now()
	mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now.Add(-time.Minute))

	const workers = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			holder := fmt.Sprintf("worker-%d", i)
			j, ok, err := s.Due(ctx, holder, now, time.Minute)
			if err != nil {
				t.Errorf("Due: %v", err)
				return
			}
			if ok {
				mu.Lock()
				winners = append(winners, j.Lease.Holder)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("%d workers took the job (%v); want exactly 1", len(winners), winners)
	}
}

// TestDueIsAtomicAcrossStoreHandles races two independent handles on the same
// file, which is the contention that actually happens: ADR 0001 §4 makes a
// hand-run `afk run` a second process alongside the worker pool.
func TestDueIsAtomicAcrossStoreHandles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := open(t, dir)
	b := open(t, dir)

	now := time.Now()
	mustEnsure(t, a, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now.Add(-time.Minute))

	type result struct {
		ok  bool
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, s := range []store.Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.Due(ctx, fmt.Sprintf("handle-%d", i), now, time.Minute)
			results <- result{ok, err}
		}()
	}
	wg.Wait()
	close(results)

	got := 0
	for r := range results {
		if r.err != nil {
			t.Fatalf("Due: %v", r.err)
		}
		if r.ok {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("%d handles took the job; want exactly 1", got)
	}
}

func TestDueSkipsWhatIsNotScheduled(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()

	// Not scheduled at all, and scheduled for later. Neither is due.
	mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 1}, "start", time.Time{})
	mustEnsure(t, s, store.KindImplement, store.Subject{Type: store.SubjectIssue, Number: 2}, "start", now.Add(time.Hour))

	if _, ok, err := s.Due(ctx, "worker", now, time.Minute); err != nil || ok {
		t.Fatalf("Due = %v, %v; want false, nil", ok, err)
	}
}

func TestDueTakesTheLongestWaitingFirst(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()

	mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 1}, "start", now.Add(-time.Minute))
	older := mustEnsure(t, s, store.KindImplement, store.Subject{Type: store.SubjectIssue, Number: 2}, "start", now.Add(-time.Hour))

	j, ok, err := s.Due(ctx, "worker", now, time.Minute)
	if err != nil || !ok {
		t.Fatalf("Due = %v, %v; want true, nil", ok, err)
	}
	if j.ID != older.ID {
		t.Errorf("Due took %s; want the longest-waiting %s", j.ID, older.ID)
	}
}

// --- Acceptance: a commit is refused unless the lease is still held. ---

func TestCommitRequiresTheLease(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)

	if _, ok, _ := s.Acquire(ctx, j.ID, "worker-a", now, time.Minute); !ok {
		t.Fatal("worker-a could not acquire")
	}
	err := s.Commit(ctx, store.Commit{JobID: j.ID, Holder: "worker-b", State: "done"})
	if !errors.Is(err, store.ErrNotHeld) {
		t.Fatalf("Commit by a non-holder = %v; want ErrNotHeld", err)
	}

	// And nothing was applied.
	after, err := s.Job(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "start" {
		t.Errorf("state = %q after a refused commit; want it untouched", after.State)
	}
}

// TestCommitAfterExpiryIsAllowedUntilSomeoneElseTakesIt pins a deliberate
// choice. A lease that lapsed but that nobody has taken still commits: the work
// was done, and throwing it away would be a second failure on top of being
// slow. What the lease protects against is two holders, and that is caught by
// the holder check rather than by the clock.
func TestCommitAfterExpiryIsAllowedUntilSomeoneElseTakesIt(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)
	if _, ok, _ := s.Acquire(ctx, j.ID, "worker-a", now, time.Minute); !ok {
		t.Fatal("could not acquire")
	}

	// Well past expiry, but nothing else has been near the job.
	if err := s.Commit(ctx, store.Commit{JobID: j.ID, Holder: "worker-a", State: "done"}); err != nil {
		t.Fatalf("Commit after expiry = %v; want it to succeed", err)
	}

	// Once another worker has taken it, though, the slow one is shut out.
	if _, ok, _ := s.Acquire(ctx, j.ID, "worker-b", now.Add(2*time.Minute), time.Minute); !ok {
		t.Fatal("worker-b could not reclaim the expired lease")
	}
	err := s.Commit(ctx, store.Commit{JobID: j.ID, Holder: "worker-a", State: "clobbered"})
	if !errors.Is(err, store.ErrNotHeld) {
		t.Fatalf("Commit after another worker took over = %v; want ErrNotHeld", err)
	}
}

func TestCommitReleasesAndSchedules(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)
	if _, ok, _ := s.Acquire(ctx, j.ID, "worker", now, time.Minute); !ok {
		t.Fatal("could not acquire")
	}

	next := now.Add(15 * time.Minute).Truncate(time.Nanosecond)
	err := s.Commit(ctx, store.Commit{
		JobID: j.ID, Holder: "worker",
		State: "awaiting-ci", Attempts: 3, NextRunAt: next, Release: true,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := s.Job(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "awaiting-ci" || got.Attempts != 3 {
		t.Errorf("got state %q attempts %d; want awaiting-ci, 3", got.State, got.Attempts)
	}
	if !got.NextRunAt.Equal(next) {
		t.Errorf("NextRunAt = %v; want %v", got.NextRunAt, next)
	}
	if got.Lease != nil {
		t.Errorf("lease = %+v; want released", got.Lease)
	}
}

// --- Acceptance: Ensure is idempotent, so re-deriving never resets work. ---

func TestEnsureDoesNotResetWorkAlreadyUnderWay(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	subj := store.Subject{Type: store.SubjectPR, Number: 12}
	j := mustEnsure(t, s, store.KindReview, subj, "start", now)

	if _, ok, _ := s.Acquire(ctx, j.ID, "worker", now, time.Minute); !ok {
		t.Fatal("could not acquire")
	}
	if err := s.Commit(ctx, store.Commit{JobID: j.ID, Holder: "worker", State: "in-review", Attempts: 2}); err != nil {
		t.Fatal(err)
	}

	// The next pass over the tracker sees the same subject and calls Ensure
	// again. It must not undo the progress above.
	again := mustEnsure(t, s, store.KindReview, subj, "start", now)
	if again.State != "in-review" || again.Attempts != 2 {
		t.Errorf("Ensure reset the job to %q/%d; want in-review/2", again.State, again.Attempts)
	}
}

func TestEnsureRejectsUnknownKindAndSubject(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	if _, err := s.Ensure(ctx, store.Kind("deploy"), store.Subject{Type: store.SubjectPR, Number: 1}, "start", time.Now()); err == nil {
		t.Error("Ensure accepted an unknown kind; want an error")
	}
	if _, err := s.Ensure(ctx, store.KindReview, store.Subject{Type: store.SubjectType("discussion"), Number: 1}, "start", time.Now()); err == nil {
		t.Error("Ensure accepted an unknown subject type; want an error")
	}
}

func TestJobReportsErrNoJob(t *testing.T) {
	s := open(t, t.TempDir())
	if _, err := s.Job(context.Background(), "review-pr-999"); !errors.Is(err, store.ErrNoJob) {
		t.Fatalf("Job on a missing id = %v; want ErrNoJob", err)
	}
}

// --- Acceptance: at most one outward effect, replaying after a crash. ---

var errCrash = errors.New("crash")

// transition is a stand-in for a real transition: it performs exactly one
// outward effect (posting a comment), guarded by an idempotency key. crashAt
// stops it at a numbered point, standing in for the process dying there.
//
// now is a parameter because the replacement worker does not run at the same
// instant as the one that died - it runs once the dead holder's lease has
// lapsed, which is the only way it can take the job at all.
//
// The key is reserved in the same commit as the state change and *before* the
// effect, which is the ordering Store.Commit documents: a crash between the
// two loses the effect, and the alternative ordering duplicates it.
func transition(ctx context.Context, s store.Store, id, holder string, now time.Time, effects *int, crashAt int) error {
	crash := func(n int) bool { return crashAt == n }

	if _, ok, err := s.Acquire(ctx, id, holder, now, time.Minute); err != nil {
		return err
	} else if !ok {
		return errors.New("could not acquire")
	}
	if crash(1) {
		return errCrash
	}

	j, err := s.Job(ctx, id)
	if err != nil {
		return err
	}
	key := "comment:" + id + ":handoff"

	reserved, err := s.Reserved(ctx, key)
	if err != nil {
		return err
	}
	if crash(2) {
		return errCrash
	}

	if !reserved {
		if err := s.Commit(ctx, store.Commit{
			JobID: id, Holder: holder, State: "posting", Attempts: j.Attempts + 1, Keys: []string{key},
		}); err != nil {
			return err
		}
		if crash(3) {
			return errCrash
		}
		*effects++ // the outward effect: one comment on the pull request
	}
	if crash(4) {
		return errCrash
	}

	return s.Commit(ctx, store.Commit{
		JobID: id, Holder: holder, State: "handed-off", Attempts: j.Attempts + 1, Release: true,
	})
}

func TestReplayAfterACrashProducesAtMostOneEffect(t *testing.T) {
	ctx := context.Background()
	for crashAt := 1; crashAt <= 4; crashAt++ {
		t.Run(fmt.Sprintf("crash_at_%d", crashAt), func(t *testing.T) {
			s := open(t, t.TempDir())
			now := time.Now()
			j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)

			effects := 0
			if err := transition(ctx, s, j.ID, "worker-a", now, &effects, crashAt); !errors.Is(err, errCrash) {
				t.Fatalf("run one = %v; want errCrash", err)
			}

			// worker-a died holding the lease. The replacement can only take
			// the job once that lease has lapsed, so it runs later - which is
			// the reclamation path the expiry criterion asks for, exercised
			// here rather than asserted separately.
			later := now.Add(2 * time.Minute)
			if err := transition(ctx, s, j.ID, "worker-b", later, &effects, 0); err != nil {
				t.Fatalf("replay: %v", err)
			}
			// And once more, because a replay can itself be replayed.
			if err := transition(ctx, s, j.ID, "worker-b", later.Add(time.Second), &effects, 0); err != nil {
				t.Fatalf("second replay: %v", err)
			}

			if effects > 1 {
				t.Fatalf("crash at %d then replay produced %d outward effects; want at most 1", crashAt, effects)
			}

			// Crashing at 3 is the documented loss: the key was reserved, the
			// effect never happened, and the replay correctly declines to
			// repeat what it cannot tell it did not do. Zero, never two.
			if crashAt == 3 && effects != 0 {
				t.Errorf("crash between reserving the key and the effect produced %d effects; want 0", effects)
			}
			if crashAt != 3 && effects != 1 {
				t.Errorf("crash at %d produced %d effects; want 1 after replay", crashAt, effects)
			}
		})
	}
}

func TestReservedIsFalseUntilCommitted(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	if got, err := s.Reserved(ctx, "comment:review-pr-12:handoff"); err != nil || got {
		t.Fatalf("Reserved on a fresh store = %v, %v; want false, nil", got, err)
	}
}

// TestKeysAreNotReservedByARefusedCommit is the "same transaction" criterion
// from the other side: a commit that is refused must leave no key behind, or a
// replay would skip an effect that never happened.
func TestKeysAreNotReservedByARefusedCommit(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	now := time.Now()
	j := mustEnsure(t, s, store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}, "start", now)
	if _, ok, _ := s.Acquire(ctx, j.ID, "worker-a", now, time.Minute); !ok {
		t.Fatal("could not acquire")
	}

	key := "comment:review-pr-12:handoff"
	err := s.Commit(ctx, store.Commit{JobID: j.ID, Holder: "worker-b", State: "posting", Keys: []string{key}})
	if !errors.Is(err, store.ErrNotHeld) {
		t.Fatalf("Commit = %v; want ErrNotHeld", err)
	}
	if reserved, err := s.Reserved(ctx, key); err != nil || reserved {
		t.Fatalf("a refused commit reserved %q (%v, %v); want it not reserved", key, reserved, err)
	}
}

// --- Acceptance: the schema is re-creatable from empty. ---

// tracker stands in for GitHub: the eligible subjects, which the agent re-reads
// rather than remembers. Nothing here comes out of the store.
type tracker []struct {
	kind    store.Kind
	subject store.Subject
}

// rederive is the queue being rebuilt from the tracker, which is what happens
// on every pass and, in particular, on the first pass after the store is gone.
func (tr tracker) rederive(ctx context.Context, s store.Store, now time.Time) error {
	for _, e := range tr {
		if _, err := s.Ensure(ctx, e.kind, e.subject, "start", now); err != nil {
			return err
		}
	}
	return nil
}

func TestWipingTheStoreMidRunLosesSchedulingNotCorrectness(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	now := time.Now()

	tr := tracker{
		{store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}},
		{store.KindImplement, store.Subject{Type: store.SubjectIssue, Number: 3}},
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := tr.rederive(ctx, s, now); err != nil {
		t.Fatalf("rederive: %v", err)
	}

	// Get a run genuinely under way: a lease, a state change, a reserved key,
	// and a scheduled re-entry.
	id := store.ID(store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12})
	if _, ok, _ := s.Acquire(ctx, id, "worker-a", now, time.Hour); !ok {
		t.Fatal("could not acquire")
	}
	key := "comment:" + id + ":handoff"
	if err := s.Commit(ctx, store.Commit{
		JobID: id, Holder: "worker-a", State: "awaiting-ci", Attempts: 2,
		NextRunAt: now.Add(time.Hour), Keys: []string{key},
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Wipe it, the way an operator or a fresh host would: the file and the
	// WAL and shared-memory sidecars that go with it.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("wipe %s: %v", path+suffix, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("store still present after the wipe")
	}

	// The binary brings its own schema forward, so re-opening an absent store
	// is not an error and needs no operator action.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen after wipe: %v", err)
	}
	defer s2.Close()
	if err := tr.rederive(ctx, s2, now); err != nil {
		t.Fatalf("rederive after wipe: %v", err)
	}

	// Correctness survives: every job the tracker knows about is back, under
	// the same id, so `afk run --job` still names the same work.
	jobs, err := s2.Jobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != len(tr) {
		t.Fatalf("%d jobs after re-derivation; want %d", len(jobs), len(tr))
	}
	back, err := s2.Job(ctx, id)
	if err != nil {
		t.Fatalf("the job did not re-derive under its old id: %v", err)
	}

	// Scheduling and dedup history are what the wipe cost, and that is the
	// documented trade rather than a defect: the state came back from the
	// tracker's initial state, not from the store.
	if back.Lease != nil {
		t.Errorf("lease survived the wipe: %+v", back.Lease)
	}
	if back.Attempts != 0 {
		t.Errorf("attempts = %d after the wipe; want 0", back.Attempts)
	}
	if reserved, err := s2.Reserved(ctx, key); err != nil || reserved {
		t.Errorf("idempotency key survived the wipe (%v, %v); want it lost", reserved, err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	for i := range 3 {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if _, err := s.Jobs(context.Background()); err != nil {
			t.Fatalf("Jobs after Open %d: %v", i, err)
		}
		s.Close()
	}
}

func TestOpenCreatesTheStoreInAnEmptyDirectory(t *testing.T) {
	s := open(t, t.TempDir())
	jobs, err := s.Jobs(context.Background())
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("%d jobs in a fresh store; want 0", len(jobs))
	}
}

func TestIDIsStableAndDistinguishesTheNumberSpace(t *testing.T) {
	// GitHub shares one number space across issues and pull requests, so issue
	// 12 and pull request 12 are different subjects and must not collide.
	issue := store.ID(store.KindReview, store.Subject{Type: store.SubjectIssue, Number: 12})
	pr := store.ID(store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12})
	if issue == pr {
		t.Fatalf("issue and pull request 12 share the id %q", issue)
	}
	if again := store.ID(store.KindReview, store.Subject{Type: store.SubjectPR, Number: 12}); again != pr {
		t.Errorf("ID is not stable: %q then %q", pr, again)
	}
}
