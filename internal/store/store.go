// Package store is the job store: leases, scheduling, and idempotency history.
//
// It holds run state only, never work state (ADR 0001 §5). What is to be done,
// and what a human said about it, stays in GitHub and is re-read. The store is
// disposable and is not backed up (ADR 0001 §6): deleting it loses scheduling
// and deduplication history, never correctness.
//
// That split is the invariant worth a reviewer's attention, and it is the one
// that decays quietly - a column added here to save a round trip to GitHub is
// how it goes. TestNoRederivableColumns guards it.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Errors callers are expected to distinguish.
var (
	// ErrNoJob is returned for a job id that is not in the store.
	ErrNoJob = errors.New("no such job")

	// ErrNotHeld is returned when a commit names a holder that is not the one
	// recorded against the job - because another worker reclaimed the lease
	// after it expired, or because it was never held. The commit is refused
	// rather than applied, so two holders can never both write.
	//
	// Expiry alone does not refuse a commit. A holder that overran its lease
	// but that nobody displaced still commits: the work was done, and dropping
	// it would be a second failure on top of being slow. What must not happen
	// is two writers, and the holder check is what prevents that.
	ErrNotHeld = errors.New("lease not held")
)

// Kind is what a job is for (CONTEXT.md: job kind). It determines which
// transitions the job may take and what it requires of a model.
type Kind string

const (
	KindImplement Kind = "implement"
	KindReview    Kind = "review"
	KindRevise    Kind = "revise"
)

// Valid reports whether k is a kind this agent knows.
func (k Kind) Valid() bool {
	switch k {
	case KindImplement, KindReview, KindRevise:
		return true
	}
	return false
}

// SubjectType distinguishes the two things a job can be attached to. GitHub
// shares one number space across issues and pull requests, so the number alone
// does not identify a subject.
type SubjectType string

const (
	SubjectIssue SubjectType = "issue"
	SubjectPR    SubjectType = "pr"
)

// Valid reports whether t is a subject type this agent knows.
func (t SubjectType) Valid() bool {
	return t == SubjectIssue || t == SubjectPR
}

// Subject is the tracker subject a job is attached to. A job has exactly one
// (CONTEXT.md: job).
type Subject struct {
	Type   SubjectType
	Number int
}

// String renders a subject the way the tracker spells it.
func (s Subject) String() string {
	return fmt.Sprintf("%s %d", s.Type, s.Number)
}

// Lease is a local, exclusive, expiring hold on a job, held by the process
// executing a transition (CONTEXT.md: lease). It is not a claim: a claim is the
// tracker-visible marker that the work is taken, lives in GitHub, has no owner
// and no expiry, and this package does not model it.
type Lease struct {
	Holder    string
	ExpiresAt time.Time
}

// Expired reports whether the lease has lapsed as at now, which is what makes a
// dead holder's job reclaimable without operator action.
func (l Lease) Expired(now time.Time) bool {
	return !now.Before(l.ExpiresAt)
}

// Job is a durable, resumable piece of agent work attached to exactly one
// tracker subject (CONTEXT.md: job).
//
// State is opaque here. Which states exist and which transitions connect them
// belongs to the transition runner (#2); the store persists the name and does
// not interpret it, so adding a state is not a change to this package.
type Job struct {
	ID       string
	Kind     Kind
	Subject  Subject
	State    string
	Attempts int

	// Stays is how many runs have decided to stay in State since the job
	// entered it. A run that returned an error is an attempt and not a stay,
	// and leaves the count where it was; a move sets both back to zero. The
	// review and implement kinds choose their candidate model by it, so an
	// error that is not the model's - the tracker, a clone - leaves the next
	// run on the same candidate (#62).
	Stays int

	// NextRunAt is when the job becomes due for re-entry. Zero means the job
	// is not scheduled and Due will never return it - waiting is never
	// in-process (ADR 0001 §3), so a job with nothing scheduled is a job
	// nothing is going to pick up.
	NextRunAt time.Time

	// Lease is the hold on this job, or nil if nothing holds it. A non-nil
	// lease may still be expired; see Lease.Expired.
	Lease *Lease
}

// Held reports whether holder holds this job's lease as at now.
func (j Job) Held(holder string, now time.Time) bool {
	return j.Lease != nil && j.Lease.Holder == holder && !j.Lease.Expired(now)
}

// Commit is one job's state change, applied atomically.
//
// Keys are the idempotency keys for the outward effects the transition is about
// to perform. They are reserved in the same transaction as the state change,
// before the effect happens, which is what the issue means by "recorded in the
// same transaction as the state change that produced the side-effect, not after
// it". See Store.Commit for why the ordering is that way round.
type Commit struct {
	JobID  string
	Holder string

	// State, Attempts, Stays and NextRunAt replace the job's, all four: a
	// writer that leaves one out sets it to its zero value. A writer that
	// means to keep one copies it from the job it read.
	State     string
	Attempts  int
	Stays     int
	NextRunAt time.Time

	Keys []string

	// Release drops the lease as part of the same transaction. A transition
	// that has finished its work releases; one that still has effects to
	// perform, or is handing over to its own next step, keeps the lease and
	// lets it expire on its own if the process dies.
	Release bool
}

// Store is the job store. The SQLite implementation in this package is the only
// one today; the interface exists so that a transition can be tested against a
// store it constructs rather than a file it has to clean up.
type Store interface {
	// Ensure returns the job for this kind and subject, creating it in the
	// initial state if it is not already there. It is how work enters the
	// store, and it is idempotent: re-deriving the queue from GitHub calls it
	// for every eligible subject on every pass, and an existing job is
	// returned untouched rather than reset.
	Ensure(ctx context.Context, kind Kind, subject Subject, initialState string, runAt time.Time) (Job, error)

	// Job returns one job by id, or ErrNoJob.
	Job(ctx context.Context, id string) (Job, error)

	// Jobs returns every job, oldest scheduling first. This is the operator's
	// "what is happening right now" query, which ADR 0001 notes as a cost of
	// scheduled re-entry.
	Jobs(ctx context.Context) ([]Job, error)

	// Acquire takes the lease on a named job for holder until now.Add(ttl),
	// reporting false if another live holder has it. It is what makes `afk run`
	// against a specific job safe while a worker pool is running.
	Acquire(ctx context.Context, id, holder string, now time.Time, ttl time.Duration) (Job, bool, error)

	// Due takes the lease on the longest-waiting job that is scheduled at or
	// before now and is not held by a live holder, reporting false if there is
	// no such job. Selecting and leasing are one atomic step, so two workers
	// calling Due concurrently cannot both get the same job.
	Due(ctx context.Context, holder string, now time.Time, ttl time.Duration) (Job, bool, error)

	// Commit applies a state change and reserves its idempotency keys in one
	// transaction, or returns ErrNotHeld and applies nothing.
	//
	// The effect is performed *after* Commit returns, not before. A crash
	// between the two loses the effect, and a replay finds the key already
	// reserved and skips it - so a transition replayed after a crash at any
	// point produces at most one outward effect, which is the guarantee the
	// issue asks for. The other ordering would produce two.
	//
	// A lost effect is recoverable and a duplicate one is not: the next
	// transition re-reads GitHub (ADR 0001 §5), so a comment that never landed
	// is visible as absent, while a comment posted twice cannot be un-posted.
	Commit(ctx context.Context, c Commit) error

	// Reserved reports whether key has already been reserved, so a transition
	// can skip an effect it may already have performed.
	Reserved(ctx context.Context, key string) (bool, error)

	// Release drops holder's lease on a job without changing its state. A
	// transition that panics releases rather than making the next worker wait
	// out the full lease.
	Release(ctx context.Context, id, holder string) error

	// Close releases the store's handle on its file.
	Close() error
}

// ID is the job id for a kind and subject.
//
// Derived rather than allocated, so that it is stable across a wipe of the
// store: `afk run review --job review-pr-12` names the same job after the file
// is deleted and the queue re-derived as it did before. It is also the primary
// key, which is what makes Ensure idempotent without a separate lookup.
func ID(kind Kind, subject Subject) string {
	return fmt.Sprintf("%s-%s-%d", kind, subject.Type, subject.Number)
}
