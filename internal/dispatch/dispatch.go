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

	"github.com/corygyarmathy/afk-agent/internal/budget"
	"github.com/corygyarmathy/afk-agent/internal/notify"
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

	// Budget is admission control (ADR 0001 §11). Nil is no admission control,
	// which is the shape of an unconfigured budget and is safe: work then runs
	// into the provider's limits and they arrive as transient failures, which
	// is what ADR 0001 §12 already accepts for the pay-as-you-go balance.
	//
	// It sits here and not in the runner, so `afk run` is unaffected. A
	// hand-invocation is an operator deliberately asking for this job now;
	// admission is about the pool starting work on its own (CONTEXT.md).
	Budget *budget.Observer

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

	// Notify is the operator's interrupt channel (ADR 0001 §13). Nil is no
	// notification at all, which is the shape of an unconfigured channel and is
	// safe: both conditions it carries are also states the operator can query,
	// and a pool with no notifier is one that must be looked at rather than one
	// that goes wrong.
	//
	// It sits here and not in the runner, alongside Budget and for the same
	// reason: `afk run` is a hand-invocation by an operator who is already
	// watching the output, and a notification exists for the times nobody is.
	Notify *notify.Notifier

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

// notify publishes one condition, best effort.
//
// A notification that could not be sent is a log line and changes nothing: the
// park is already in the store and the budget is already readable with `afk
// budget`, so the event it reports survives the channel failing to carry it.
//
// Without the caller's cancellation, for the reason the runner's commit is: a
// SIGTERM arriving between the store write and this must not be what turns a
// parked job into a silent one.
func (d *Dispatcher) notify(ctx context.Context, holder string, send func(context.Context) error) {
	if d.Notify == nil {
		return
	}
	if err := send(context.WithoutCancel(ctx)); err != nil {
		d.logf("%s: %v", holder, err)
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
	case d.Budget != nil && d.Budget.MaxAge <= 0:
		return errors.New("budget observation has no maximum age: every job dispatched would be a request to the usage endpoint")
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
			if !d.admit(ctx, holder, job) {
				continue
			}
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
		cause := fmt.Errorf("no transition runs from state %q", job.State)
		d.logf("%s: %s is in state %q with no transition from it; parking it", holder, job.ID, job.State)
		// Only if it actually parked. A commit that failed leaves the job due,
		// and this is the one notification that would otherwise be suppressed
		// on the pass that has something true to say: the key is the job, its
		// state and its attempts, none of which the failed commit changed.
		if d.park(ctx, holder, job) {
			d.notify(ctx, holder, func(c context.Context) error { return d.Notify.Parked(c, job, cause) })
		}
		return
	}

	tokenCtx, cancel := context.WithTimeout(ctx, d.TokenWait)
	release, err := d.Pool.Acquire(tokenCtx, t.Tokens)
	cancel()
	if err != nil {
		// Give the job back untouched. It is still due, so it is not lost;
		// something else is using the permit, and holding a lease while
		// queuing for it only stops another worker from getting there first.
		d.release(ctx, holder, job)
		d.wait(ctx)
		return
	}
	defer release()

	out, err := runner.Run(ctx, t.Name, job.ID)
	if err != nil {
		d.logf("%s: %s: %v", holder, job.ID, err)
	} else {
		d.logf("%s: %s", holder, out)
	}

	// A failure that came to rest is the one job outcome that reaches the
	// operator. Both halves of the condition matter: a failure the backoff
	// rescheduled is retrying and is not yet anyone's problem, and a park with
	// no error is a transition that chose to wait for a human - a hand-back is
	// that, and ADR 0001 §13 rules it out of this channel explicitly.
	//
	// One case reads as a hand-back and is caught here anyway, on purpose: a
	// transition that parked deliberately and whose outward effect then failed
	// (Runner.apply returns the committed outcome alongside that error). The
	// state moved and the job is resting, but the comment that was to tell the
	// human never posted - so the tracker says nothing and this channel is all
	// that is left. A hand-back that published its comment is silent, which is
	// the rule; a hand-back nobody was told about is not.
	if err != nil && out.Parked {
		d.notify(ctx, holder, func(c context.Context) error { return d.Notify.Parked(c, out.Job, err) })
	}
}

// admit consults the budget before a job starts, and gives the job back if it
// may not (ADR 0001 §11). It reports whether the job may be dispatched.
//
// After the job is taken rather than before, for two reasons. Deferring needs a
// job to reschedule, and taking the lease to give it straight back is exactly
// what the resource token path above does when a permit is not free - one shape
// for "this worker cannot run this job right now", not two.
//
// Work in flight is untouched either way. This runs between transitions, and a
// transition that has already started is not interrupted by anything here.
func (d *Dispatcher) admit(ctx context.Context, holder string, job store.Job) bool {
	if d.Budget == nil {
		return true
	}

	adm, err := d.Budget.Admit(ctx)
	if err != nil {
		// Reported rather than acted on: Admit's decision is usable whether or
		// not the refresh worked, and a usage endpoint that is down is not an
		// account that is spent.
		d.logf("%s: reading the budget: %v", holder, err)
	}
	if adm.Starts() {
		return true
	}

	d.logf("%s: %s: %s", holder, job.ID, adm)

	// Exhaustion reaches the operator; approaching a limit does not. The window
	// admission named is the one to ask, because it is the window that decided
	// the answer - a threshold Wait names a window that is merely full, and a
	// limit names one the provider has closed.
	if adm.Window.Limited() {
		w := adm.Window
		d.notify(ctx, holder, func(c context.Context) error { return d.Notify.Exhausted(c, w) })
	}

	if adm.Decision == budget.Defer {
		// No wait afterwards: the rest of the due queue is deferred on the
		// following passes, and once it is drained there is nothing due and the
		// worker waits on its own. Deferring is what stops this being a poll
		// that suppresses - the queue goes quiet until the window reopens, and
		// an operator reading it sees jobs due at that timestamp rather than
		// jobs that look due now and never run.
		//
		// A defer and not a park: Admit never returns a zero or past Until, so
		// this always schedules the job for a time it comes back at.
		d.reschedule(ctx, holder, job, adm.Until)
		return false
	}
	d.release(ctx, holder, job)
	d.wait(ctx)
	return false
}

// release gives a job back untouched. It is still due, so it is not lost.
func (d *Dispatcher) release(ctx context.Context, holder string, job store.Job) {
	if err := d.Store.Release(context.WithoutCancel(ctx), job.ID, holder); err != nil {
		d.logf("%s: releasing %s: %v", holder, job.ID, err)
	}
}

// park leaves a job in its persisted state with nothing scheduled, resting
// until an operator moves it (CONTEXT.md: park).
//
// Spelled as scheduling it for the zero time, because that is what a park is in
// the store: no lease, no next run. It is a separate name from reschedule
// because it is the opposite thing - a rescheduled job comes back on its own
// and a parked one does not - and a call reading `reschedule(job, time.Time{})`
// says the first while meaning the second.
func (d *Dispatcher) park(ctx context.Context, holder string, job store.Job) bool {
	return d.reschedule(ctx, holder, job, time.Time{})
}

// reschedule moves when a job next becomes due, leaving its state and its
// attempt count alone.
//
// Attempts are untouched on purpose. Neither parking a job nothing can move nor
// deferring one to a budget window is an attempt at the work, and counting
// either would spend the retry bound on the account being busy - a long enough
// rate-limit window would then park every job in the queue for a human to find.
// It reports whether the store took the change, for the one caller that has
// something to do about it: a park that did not commit is a job still due, and
// notifying an operator that it "stays there until someone moves it" would be
// a message that is not true about a job the next pass picks up again.
func (d *Dispatcher) reschedule(ctx context.Context, holder string, job store.Job, at time.Time) bool {
	// Without the cancellation, for the same reason the runner's commit is:
	// a stop that arrives here must not leave the lease standing.
	err := d.Store.Commit(context.WithoutCancel(ctx), store.Commit{
		JobID:     job.ID,
		Holder:    holder,
		State:     job.State,
		Attempts:  job.Attempts,
		NextRunAt: at,
		Release:   true,
	})
	if err != nil {
		d.logf("%s: rescheduling %s: %v", holder, job.ID, err)
		return false
	}
	return true
}
