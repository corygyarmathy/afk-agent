package transition

import (
	"context"
	"errors"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
)

// Armer makes jobs due under a lease of its own, for whatever asks for work
// outside the job's own transitions: a command, which intake arms from its own
// pass rather than from a transition, or another job, which arms through an
// effect (see Effect), so the runner performs it after that job's commit.
type Armer struct {
	Store    store.Store
	Holder   string
	LeaseTTL time.Duration
}

// Restart is a human asking for the work afresh: it makes the job of kind for
// subject due at now, in state start with its attempts cleared, wherever the
// job came to rest, and reserves key in the same commit. It reports false,
// having changed nothing, when the job is already queued or a live process
// holds it: that run will meet whatever asked.
//
// A job that is not there yet is created at rest and then armed like any
// other, so that the key is always reserved by the commit that made the job
// due. A crash between the two leaves a job at rest with nothing reserved, and
// the next ask arms it.
func (a Armer) Restart(ctx context.Context, kind store.Kind, subject store.Subject, start string, now time.Time, key string) (store.Job, bool, error) {
	job, err := a.Store.Ensure(ctx, kind, subject, start, time.Time{})
	if err != nil {
		return store.Job{}, false, err
	}
	return a.arm(ctx, job, start, now, []string{key}, func(job store.Job) bool {
		return job.NextRunAt.IsZero()
	})
}

// Ask is another job asking for the work: it makes the job of kind for subject
// due at now, in state start with its attempts cleared, if it is at rest in
// start. A job already queued or held is left alone, since that run will meet
// whatever asked, and so is one parked in any other state, which is the
// operator's to look at. A job that is not there yet is created due, so that a
// crash straight after leaves it queued.
func (a Armer) Ask(ctx context.Context, kind store.Kind, subject store.Subject, start string, now time.Time) error {
	job, err := a.Store.Ensure(ctx, kind, subject, start, now)
	if err != nil {
		return err
	}
	_, _, err = a.arm(ctx, job, start, now, nil, func(job store.Job) bool {
		return job.NextRunAt.IsZero() && job.State == start
	})
	return err
}

// arm takes the lease on job, checks it is still armable, and commits it due
// at now in state start with keys reserved.
func (a Armer) arm(ctx context.Context, job store.Job, start string, now time.Time, keys []string, armable func(store.Job) bool) (store.Job, bool, error) {
	if !armable(job) {
		return store.Job{}, false, nil
	}

	job, ok, err := a.Store.Acquire(ctx, job.ID, a.Holder, now, a.LeaseTTL)
	if err != nil || !ok {
		return store.Job{}, false, err
	}
	if !armable(job) {
		// Scheduled between the read and the lease - by `afk run`, say.
		return store.Job{}, false, a.Store.Release(ctx, job.ID, a.Holder)
	}

	job.State = start
	job.Attempts = 0
	job.Stays = 0
	job.NextRunAt = now
	// Without the cancellation, for the reason the runner's commit is: a stop
	// arriving here must not leave the lease standing.
	err = a.Store.Commit(context.WithoutCancel(ctx), store.Commit{
		JobID:     job.ID,
		Holder:    a.Holder,
		State:     job.State,
		Attempts:  job.Attempts,
		Stays:     job.Stays,
		NextRunAt: job.NextRunAt,
		Keys:      keys,
		Release:   true,
	})
	if err != nil {
		return store.Job{}, false, errors.Join(err, a.Store.Release(context.WithoutCancel(ctx), job.ID, a.Holder))
	}
	job.Lease = nil
	return job, true, nil
}
