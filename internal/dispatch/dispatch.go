// Package dispatch is the worker pool: it finds jobs that are due and hands
// them to the transition runner.
//
// It is deliberately thin, and it is not where the interesting properties live.
// A transition does not need it (ADR 0001 §4) - `afk run` reaches exactly the
// same runner - so this package adds only two things: several transitions
// executing at once, and the resource tokens that stop the heavy ones from
// doing so.
//
// Those two are separate limits (ADR 0001 §8). Worker parallelism is how many
// transitions may execute at all; token capacity is how many of a particular
// kind may execute at once. Collapsing them into one number would mean either
// running one job at a time because a build might come along, or running eight
// builds because most jobs are network-bound.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
	"github.com/corygyarmathy/afk-agent/internal/transition"
)

// Dispatcher runs transitions for jobs that are due.
//
// It embeds the transition.Runner every worker runs, so the execution path's
// fields - Store, Registry, Holder, LeaseTTL, Backoff, Clock - are declared
// once. The Runner's Holder names this process; each worker takes its lease
// under a distinct name derived from it, so "which worker is holding this" is
// answerable from the store.
type Dispatcher struct {
	transition.Runner

	Pool *transition.Pool

	// Workers is how many transitions may execute at once.
	Workers int

	// Poll and TokenWait are deployment parameters, supplied rather than
	// chosen here (AGENTS.md). Poll is how long a worker waits before looking
	// again when there is nothing due - polling is the trigger, as it was in
	// the prototype (ADR 0001 §3). TokenWait is how long a worker will hold a
	// job while waiting for its resource tokens before giving it back for
	// someone else to take; it must be shorter than LeaseTTL, or a worker can
	// still be queuing for a permit after its lease has lapsed.
	Poll      time.Duration
	TokenWait time.Duration

	// Log receives one line per dispatched job. Nil is silent: the happy path
	// does not notify (ADR 0001 §13), and this is a log rather than a
	// notification channel.
	Log func(msg string)
}

func (d *Dispatcher) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

func (d *Dispatcher) logf(format string, a ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(format, a...))
	}
}

// validate refuses a dispatcher that cannot work, at startup rather than on the
// first job. A transition declaring a token with no configured capacity is the
// one worth catching here: it would otherwise run fine until the day that
// transition is first dispatched.
func (d *Dispatcher) validate() error {
	switch {
	case d.Store == nil:
		return errors.New("dispatcher has no store")
	case d.Registry == nil:
		return errors.New("dispatcher has no registry")
	case d.Pool == nil:
		return errors.New("dispatcher has no resource token pool")
	case d.Holder == "":
		return errors.New("dispatcher has no holder name")
	case d.Workers < 1:
		return fmt.Errorf("dispatcher has %d workers", d.Workers)
	case d.LeaseTTL <= 0:
		return errors.New("dispatcher has no lease duration")
	case d.Poll <= 0:
		return errors.New("dispatcher has no poll interval")
	case d.TokenWait <= 0:
		return errors.New("dispatcher has no resource token wait")
	case d.TokenWait >= d.LeaseTTL:
		return fmt.Errorf("resource token wait %s is not shorter than the lease %s: a worker could still be queuing for a permit after its lease has lapsed", d.TokenWait, d.LeaseTTL)
	}
	for _, t := range d.Registry.All() {
		if err := d.Pool.Known(t.Tokens); err != nil {
			return fmt.Errorf("transition %q: %w", t.Name, err)
		}
	}
	return nil
} // Run starts the workers and returns when ctx is done. It is the only
// long-lived thing in this agent, and it holds no job: a worker between
// transitions has released its lease, so stopping here costs at most one
// transition per worker.
func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	for i := range d.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.work(ctx, fmt.Sprintf("%s/%d", d.Holder, i))
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// work is one worker: take the longest-waiting due job, run its transition,
// repeat.
func (d *Dispatcher) work(ctx context.Context, holder string) {
	// The dispatcher's own Runner is the template: a worker copies it and
	// names itself in the lease, so the execution path is spelled out once.
	runner := d.Runner
	runner.Holder = holder

	for ctx.Err() == nil {
		job, ok, err := d.Store.Due(ctx, holder, d.now(), d.LeaseTTL)
		switch {
		case err != nil:
			// A store that cannot be read is not a reason to take a worker
			// down; the next pass may find it readable, and a pool that has
			// quietly lost its workers looks exactly like an idle one.
			d.logf("%s: reading the queue: %v", holder, err)
			d.wait(ctx)
		case !ok:
			d.wait(ctx)
		default:
			d.dispatch(ctx, &runner, holder, job)
		}
	}
}

// wait sleeps for the poll interval, or until ctx is done.
func (d *Dispatcher) wait(ctx context.Context) {
	t := time.NewTimer(d.Poll)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// dispatch runs the transition this job's state calls for, once its resource
// tokens are free.
func (d *Dispatcher) dispatch(ctx context.Context, runner *transition.Runner, holder string, job store.Job) {
	t, ok := d.Registry.Next(job.Kind, job.State)
	if !ok {
		// Scheduled, but with nowhere to go: a terminal state that was left
		// scheduled, or a state written by a binary that knew a transition
		// this one does not. Park it rather than spin on it, and say so - a
		// job nothing can move is the operator's to look at.
		d.logf("%s: %s is in state %q with no transition from it; parking it", holder, job.ID, job.State)
		d.park(ctx, holder, job)
		return
	}

	tokenCtx, cancel := context.WithTimeout(ctx, d.TokenWait)
	release, err := d.Pool.Acquire(tokenCtx, t.Tokens)
	cancel()
	if err != nil {
		// Give the job back untouched. It is still due, so it is not lost;
		// something else is using the permit, and holding a lease while
		// queuing for it only stops another worker from getting there first.
		if rerr := d.Store.Release(context.WithoutCancel(ctx), job.ID, holder); rerr != nil {
			d.logf("%s: releasing %s: %v", holder, job.ID, rerr)
		}
		d.wait(ctx)
		return
	}
	defer release()

	out, err := runner.Run(ctx, t.Name, job.ID)
	if err != nil {
		d.logf("%s: %s: %v", holder, job.ID, err)
		return
	}
	d.logf("%s: %s", holder, out)
}

// park leaves a job in its state with nothing scheduled, so no worker picks it
// up again until something reschedules it.
func (d *Dispatcher) park(ctx context.Context, holder string, job store.Job) {
	// Without the cancellation, for the same reason the runner's commit is:
	// a stop that arrives here must not leave the lease standing.
	err := d.Store.Commit(context.WithoutCancel(ctx), store.Commit{
		JobID:    job.ID,
		Holder:   holder,
		State:    job.State,
		Attempts: job.Attempts,
		Release:  true,
	})
	if err != nil {
		d.logf("%s: parking %s: %v", holder, job.ID, err)
	}
}
