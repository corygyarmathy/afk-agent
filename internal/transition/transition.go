// Package transition is the execution core: what a transition is, the registry
// that names them, and the runner that applies one to a job.
//
// A transition is the only thing that executes (ADR 0001 §2). It takes a job in
// a persisted state and moves it to the next one, and nothing runs for longer
// than that: a job is never "in progress" between transitions. A job that must
// wait persists its state and schedules re-entry rather than sleeping
// (ADR 0001 §3), which is why a transition never blocks on CI, on a rate-limit
// window, or on a human.
//
// Two properties are structural rather than conventional, and the tests in this
// package are what keep them that way:
//
//   - A transition is invokable standalone, with no daemon present
//     (ADR 0001 §4). The runner takes a store, a registry and a job id; there
//     is no scheduler in the path, so `afk run` and the dispatcher reach a
//     transition through the identical code.
//   - A transition decides; the runner applies. A transition returns a Result
//     rather than writing to the store or to GitHub itself, so it is testable
//     from a fixture state with no network, no model and no daemon.
package transition

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/store"
)

// Transition is one unit of execution that moves a job from one persisted state
// to the next (CONTEXT.md: transition).
//
// It is a value rather than an interface because everything about it except Run
// is data the runner and the dispatcher must read *before* anything executes:
// which job kind it belongs to, which state it runs from, and which resource
// tokens it needs. An interface would let a transition compute its tokens,
// which is exactly the thing that must be knowable without running it.
type Transition struct {
	// Name is how the transition is spelled on the command line:
	// `afk run <name> --job <id>`.
	Name string

	// Kind is the job kind this transition belongs to. A job kind determines
	// which transitions the job may take (CONTEXT.md: job kind), and this is
	// that edge, read from the other end.
	Kind store.Kind

	// From is the persisted state the transition runs from. One transition per
	// (kind, state): the state a job is in is what decides what happens next,
	// so two candidates would mean the state machine is ambiguous.
	From string

	// Tokens are the resource tokens this transition must hold to run
	// (CONTEXT.md: resource token). They express a host constraint rather than
	// a logical one, so most transitions declare none: a transition that calls
	// an API holds nothing, one that builds holds the heavy-build token
	// (ADR 0001 §8). Names are matched against the pool's capacities, and an
	// unknown one is an error rather than an unlimited permit - a typo must
	// not silently become "no limit".
	Tokens []string

	// Run decides what happens next. It must not write to the store and must
	// not perform an outward effect: it returns them, and the runner applies
	// them in the order that makes a replay safe. That is what makes it
	// testable from a fixture state with nothing live present.
	Run func(ctx context.Context, in In) (Result, error)
}

// In is what a transition is given. Deliberately small: the job, and the time.
//
// Anything a transition needs from GitHub it reads for itself, because GitHub
// owns work state and the store does not duplicate it (ADR 0001 §5). Passing
// the clock rather than calling time.Now is what lets a transition's scheduling
// decisions be asserted in a test.
type In struct {
	Job store.Job
	Now time.Time
}

// Effect is one outward, non-undoable action - a comment posted, a label
// written - paired with the idempotency key that makes replaying it safe
// (ADR 0001 §5).
//
// The two are one value rather than two fields on Result because the failure
// mode worth designing out is the mismatch: a key reserved for an effect that
// never happens, or an effect performed under no key at all. There is no way to
// express either of those here.
type Effect struct {
	// Key identifies the effect across replays. It must describe the effect
	// and not the attempt - "review-pr-12-<head sha>" rather than a
	// per-invocation id - or a replay reserves a second key and posts a second
	// comment.
	Key string

	// Do performs the effect. The runner calls it after the state change is
	// committed, and only if Key was not already reserved.
	Do func(ctx context.Context) error
}

// Result is what a transition decided. It is data, so a test asserts on it
// without a store, and the runner is the only thing that can act on it.
type Result struct {
	// State is the state the job moves to. Required; a transition that means
	// to stay where it is returns its own From, which is the ordinary shape of
	// waiting - the same state, scheduled for later.
	State string

	// RunAt is when the job becomes due again. The zero time means nothing is
	// scheduled, which is how a transition says the job has come to rest:
	// finished, or handed back to a human. A job with nothing scheduled is one
	// the dispatcher will never pick up (see store.Job.NextRunAt).
	RunAt time.Time

	// Effects are the outward actions this transition is about to perform.
	Effects []Effect
}

// keys is the idempotency keys of the effects, in order.
func (r Result) keys() []string {
	if len(r.Effects) == 0 {
		return nil
	}
	keys := make([]string, len(r.Effects))
	for i, e := range r.Effects {
		keys[i] = e.Key
	}
	return keys
}

// validate reports whether a transition is well-formed. Called when a registry
// is built, so a malformed transition is a startup failure rather than a
// surprise at 04:00.
func (t Transition) validate() error {
	switch {
	case t.Name == "":
		return fmt.Errorf("transition has no name")
	case !t.Kind.Valid():
		return fmt.Errorf("transition %q: unknown job kind %q", t.Name, t.Kind)
	case t.From == "":
		return fmt.Errorf("transition %q: no state to run from", t.Name)
	case t.Run == nil:
		return fmt.Errorf("transition %q: nothing to run", t.Name)
	}
	seen := map[string]bool{}
	for _, tok := range t.Tokens {
		if tok == "" {
			return fmt.Errorf("transition %q: empty resource token name", t.Name)
		}
		if seen[tok] {
			return fmt.Errorf("transition %q: resource token %q declared twice", t.Name, tok)
		}
		seen[tok] = true
	}
	return nil
}

// stateKey is the (kind, state) a transition runs from.
type stateKey struct {
	kind  store.Kind
	state string
}

// Registry is the set of transitions this agent knows, indexed the two ways
// they are reached: by name, for a standalone invocation, and by the state a
// job is in, for the dispatcher.
//
// Immutable once built. Registration at init time through a package-level
// mutable map is the usual Go shape and is rejected here: it makes the set of
// transitions depend on which packages happen to be linked in, which is a
// property a test cannot pin down.
type Registry struct {
	byName  map[string]Transition
	byState map[stateKey]Transition
}

// NewRegistry builds a registry, rejecting a malformed or ambiguous set.
func NewRegistry(ts ...Transition) (*Registry, error) {
	r := &Registry{
		byName:  make(map[string]Transition, len(ts)),
		byState: make(map[stateKey]Transition, len(ts)),
	}
	for _, t := range ts {
		if err := t.validate(); err != nil {
			return nil, err
		}
		if _, dup := r.byName[t.Name]; dup {
			return nil, fmt.Errorf("two transitions named %q", t.Name)
		}
		k := stateKey{t.Kind, t.From}
		if other, dup := r.byState[k]; dup {
			return nil, fmt.Errorf("transitions %q and %q both run a %s job from state %q", other.Name, t.Name, t.Kind, t.From)
		}
		r.byName[t.Name] = t
		r.byState[k] = t
	}
	return r, nil
}

// MustRegistry is NewRegistry for a registry built from literals, where a
// failure is a programming error rather than a condition to handle.
func MustRegistry(ts ...Transition) *Registry {
	r, err := NewRegistry(ts...)
	if err != nil {
		panic(err)
	}
	return r
}

// Get returns the transition with this name.
func (r *Registry) Get(name string) (Transition, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Next returns the transition a job in this kind and state takes next. Absent
// means the job has no move from here, which is how a terminal state looks from
// the dispatcher's side.
func (r *Registry) Next(kind store.Kind, state string) (Transition, bool) {
	t, ok := r.byState[stateKey{kind, state}]
	return t, ok
}

// All returns every registered transition, ordered by name.
func (r *Registry) All() []Transition {
	ts := make([]Transition, 0, len(r.byName))
	for _, t := range r.byName {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	return ts
}

// Names returns every registered transition's name, ordered.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
