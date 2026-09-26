package transition

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
)

// Errors a caller is expected to distinguish.
var (
	// ErrUnknownTransition is returned for a name no transition is registered
	// under - an operator's typo, or a transition that has not been written.
	ErrUnknownTransition = errors.New("unknown transition")

	// ErrHeld is returned when a live holder has the job's lease. Expected
	// rather than exceptional: it is what `afk run` against a job a worker is
	// already executing must do, and the reason a hand-run is safe alongside a
	// running pool.
	ErrHeld = errors.New("job is held by another process")

	// ErrWrongState is returned when the job is not in the state the
	// transition runs from. A hand-invocation is the same thing as a scheduled
	// one (ADR 0001 §4), which means it is subject to the same state machine:
	// there is no "run it anyway".
	ErrWrongState = errors.New("job is not in the state this transition runs from")
)

// Backoff decides when a job re-enters after a transition failed, given the
// attempt count including the one that just failed: the runs since the job
// entered its state that returned an error. A stay is not one. Reporting false parks the
// job - it stays in its state with nothing scheduled, and no worker will pick
// it up again.
//
// A function rather than an interval because the interval is a parameter of
// this agent's deployment and belongs to the NixOS module that configures it,
// not to this package (AGENTS.md). Which model a retry reaches for, and when a
// bad few minutes at a provider becomes a deferral, is ADR 0001 §10 and lives
// with the resolver.
type Backoff func(attempts int) (time.Time, bool)

// Runner applies one transition to one job.
//
// It is the whole execution path. `afk run` constructs one and calls Run; the
// dispatcher constructs one per worker and calls Run. There is no second path,
// which is how "invokable standalone with no daemon present" stays true by
// construction rather than by discipline.
type Runner struct {
	Store    store.Store
	Registry *Registry

	// Holder names this process in a lease. It must be unique among live
	// processes and workers, or two of them will each believe they hold what
	// the other holds.
	Holder string

	// LeaseTTL is how long a lease is taken for, and how long it is renewed for
	// at a commit that has effects to perform. Long enough that a transition
	// finishes inside it, and that its effects do - the lease is held until the
	// last effect returns - short enough that a dead holder's job is
	// reclaimable without an operator. A deployment parameter, so it is supplied rather
	// than chosen here.
	LeaseTTL time.Duration

	// Backoff schedules re-entry after a failure. Nil parks a failed job,
	// which is the safe default: the alternative to no policy is not "retry
	// sensibly", it is a job that is due now, fails now, and is due again
	// immediately.
	Backoff Backoff

	// Clock is the time source. Nil means time.Now.
	Clock func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// finishing is the context the store writes that end a transition run under:
// the caller's, with its cancellation dropped.
//
// Stopping the agent must cost at most one transition, and a cancellation that
// reached the commit would cost more than that. The transition's own work is
// cancellable - that is the transition being lost, which is allowed - but the
// writes that record what happened to the job are not: a cancelled commit
// throws away a transition that had already finished, and a cancelled release
// leaves a lease standing on a process that has exited, so the next worker
// waits out a full TTL for a job nobody is working on. Neither is a thing a
// SIGTERM should do.
func finishing(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// Outcome is what one Run did, in the terms an operator reading a hand-run's
// output cares about.
type Outcome struct {
	Transition string
	Job        store.Job // the job as committed
	From       string
	To         string

	// Performed and Skipped are the idempotency keys of the effects that
	// happened and of those a previous run had already reserved.
	Performed []string
	Skipped   []string

	// Parked reports that the job came to rest with nothing scheduled, either
	// because the transition asked for that or because it failed and there is
	// no backoff policy.
	Parked bool
}

// String renders an outcome as one line.
func (o Outcome) String() string {
	s := fmt.Sprintf("%s: %s %s -> %s", o.Job.ID, o.Transition, o.From, o.To)
	switch {
	case o.Parked:
		s += ", not scheduled"
	case !o.Job.NextRunAt.IsZero():
		s += ", due " + o.Job.NextRunAt.Format(time.RFC3339)
	}
	if n := len(o.Performed); n > 0 {
		s += fmt.Sprintf(", %d effect(s)", n)
	}
	if n := len(o.Skipped); n > 0 {
		s += fmt.Sprintf(", %d already done", n)
	}
	return s
}

// Run applies the named transition to the named job.
//
// The order is the whole point of this function, and it is the order the store
// documents: take the lease, decide, commit the state change and reserve the
// keys in one transaction, then perform the outward effects, and only then give
// the lease back. A process killed at any point in that sequence loses at most
// this transition: before the commit, nothing happened and the lease expires;
// after it, the state moved and the reserved key stops a replay from performing
// the effect a second time.
//
// The lease outlives the commit, renewed for a whole LeaseTTL, because the job's
// next transition must not run while an effect is still in flight. A result due now is due the moment it
// commits, and a worker that took it then would read GitHub before the push or
// the comment had landed, find it missing, and do it again.
// A lost effect is recoverable because the next transition re-reads GitHub
// (ADR 0001 §5); a duplicated one is not.
func (r *Runner) Run(ctx context.Context, name, jobID string) (Outcome, error) {
	t, ok := r.Registry.Get(name)
	if !ok {
		return Outcome{}, fmt.Errorf("%w %q", ErrUnknownTransition, name)
	}

	now := r.now()
	job, ok, err := r.Store.Acquire(ctx, jobID, r.Holder, now, r.LeaseTTL)
	if err != nil {
		return Outcome{}, err
	}
	if !ok {
		return Outcome{}, fmt.Errorf("%w: %s", ErrHeld, jobID)
	}

	return r.apply(ctx, t, job, now)
}

// apply runs the transition and commits what it decided. Every path out of it
// that still holds the lease gives it back exactly once: through the commit's
// own release when there is nothing to perform, or through release - once the
// effects have run, or on a path that returns without a commit.
func (r *Runner) apply(ctx context.Context, t Transition, job store.Job, now time.Time) (out Outcome, err error) {
	if job.Kind != t.Kind {
		return Outcome{}, r.release(ctx, job, fmt.Errorf("transition %q runs %s jobs, %s is a %s job", t.Name, t.Kind, job.ID, job.Kind))
	}
	if job.State != t.From {
		return Outcome{}, r.release(ctx, job, fmt.Errorf("%w: %s is in state %q, transition %q runs from %q", ErrWrongState, job.ID, job.State, t.Name, t.From))
	}

	res, err := r.decide(ctx, t, job, now)
	if err != nil {
		return r.fail(ctx, t, job, now, err)
	}
	if res.State == "" {
		return r.fail(ctx, t, job, now, fmt.Errorf("transition %q returned no next state", t.Name))
	}

	// Filter before committing, not after: the keys this commit reserves are
	// the ones this run is about to perform, so an effect a previous run
	// already reserved must be dropped here or the two sets stop agreeing.
	//
	// Under `done`, not the caller's context: this check is part of the
	// commit's machinery - it decides what the commit reserves - and a
	// cancellation here would throw away a transition that had already
	// decided, which is what `finishing` exists to prevent.
	done := finishing(ctx)
	todo := make([]Effect, 0, len(res.Effects))
	var skipped []string
	for _, e := range res.Effects {
		if e.Key == "" || e.Do == nil {
			return r.fail(ctx, t, job, now, fmt.Errorf("transition %q returned an effect with no key or nothing to do", t.Name))
		}
		reserved, err := r.Store.Reserved(done, e.Key)
		if err != nil {
			return r.fail(ctx, t, job, now, err)
		}
		if reserved {
			skipped = append(skipped, e.Key)
			continue
		}
		todo = append(todo, e)
	}

	// A transition that stays where it is has decided something, and counts a
	// stay; its attempts carry over untouched, because an attempt is a run
	// that returned an error, and fail is the only place that counts one. A
	// stay that spent the retry bound would park a job on its first error
	// after a long wait (#74). One that moves has got somewhere, and both
	// counts start again.
	attempts, stays := 0, 0
	if res.State == job.State {
		attempts, stays = job.Attempts, job.Stays+1
	}

	c := store.Commit{
		JobID:     job.ID,
		Holder:    r.Holder,
		State:     res.State,
		Attempts:  attempts,
		Stays:     stays,
		NextRunAt: res.RunAt,
		Keys:      Result{Effects: todo}.keys(),
		Release:   len(todo) == 0,
	}
	if !c.Release {
		c.LeaseUntil = r.now().Add(r.LeaseTTL)
	}
	if err := r.Store.Commit(done, c); err != nil {
		return Outcome{}, r.release(ctx, job, err)
	}

	committed, err := r.Store.Job(done, job.ID)
	if err != nil {
		return Outcome{}, r.release(ctx, job, err)
	}
	out = Outcome{
		Transition: t.Name,
		Job:        committed,
		From:       job.State,
		To:         res.State,
		Skipped:    skipped,
		Parked:     res.RunAt.IsZero(),
	}

	// After the commit. An effect that fails here has not been lost quietly:
	// the state has moved, and the next transition re-reads GitHub and sees
	// the comment is not there.
	for _, e := range todo {
		if err := e.Do(ctx); err != nil {
			return out, r.release(ctx, job, fmt.Errorf("effect %q on %s: %w", e.Key, job.ID, err))
		}
		out.Performed = append(out.Performed, e.Key)
	}
	return out, r.release(ctx, job, nil)
}

// release gives the lease back on a run that ends without a commit or whose
// effects have finished, and returns the cause, which is nil for a run that
// succeeded. The store's Release is its own guard: it drops only this holder's
// lease, so a commit that already released makes this a no-op rather than a
// double release, and a lease that ran out mid-effect and was taken by another
// worker stays with that worker.
func (r *Runner) release(ctx context.Context, job store.Job, cause error) error {
	// Release rather than let the lease run out: the job is going back on the
	// queue, and making the next worker wait out a full TTL for a job this
	// process has finished with is time spent for nothing.
	if rerr := r.Store.Release(finishing(ctx), job.ID, r.Holder); rerr != nil {
		return errors.Join(cause, rerr)
	}
	return cause
}

// decide runs the transition's own code, turning a panic in it into an error.
// A panicking transition must cost its job and nothing else, which means the
// process survives it and the lease is given back rather than waited out.
func (r *Runner) decide(ctx context.Context, t Transition, job store.Job, now time.Time) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("transition %q panicked on %s: %v", t.Name, job.ID, p)
		}
	}()
	return t.Run(ctx, In{Job: job, Now: now})
}

// fail records a failed attempt against the job and schedules its re-entry, or
// parks it if there is no policy to schedule by. The original error is what the
// caller gets back either way; a store failure on top of it is joined to it
// rather than replacing it, because the first one is the one worth reading.
func (r *Runner) fail(ctx context.Context, t Transition, job store.Job, now time.Time, cause error) (Outcome, error) {
	attempts := job.Attempts + 1
	var runAt time.Time
	if r.Backoff != nil {
		if at, ok := r.Backoff(attempts); ok {
			runAt = at
		}
	}

	c := store.Commit{
		JobID:     job.ID,
		Holder:    r.Holder,
		State:     job.State,
		Attempts:  attempts,
		Stays:     job.Stays,
		NextRunAt: runAt,
		Release:   true,
	}
	done := finishing(ctx)
	if err := r.Store.Commit(done, c); err != nil {
		return Outcome{}, r.release(ctx, job, errors.Join(cause, err))
	}

	committed, err := r.Store.Job(done, job.ID)
	if err != nil {
		return Outcome{}, r.release(ctx, job, errors.Join(cause, err))
	}
	out := Outcome{
		Transition: t.Name,
		Job:        committed,
		From:       job.State,
		To:         job.State,
		Parked:     runAt.IsZero(),
	}
	return out, cause
}
