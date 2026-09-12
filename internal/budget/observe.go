package budget

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/fetch"
)

// Observer is the fetch seam and the last good answer: the one thing in this
// package that reaches the network, kept apart from State.Admit so the policy
// stays pure.
//
// It holds the last observation in memory and nowhere else. The store holds run
// state, and a budget observation is re-derivable from the provider by
// definition (ADR 0001 §5, §6) - persisting it would buy nothing anyway, since
// what it says expires in minutes and a restart re-reads it.
type Observer struct {
	// Fetch retrieves the document. Nil means HTTP to Endpoint with Token. A
	// field rather than a package-level function because "the tests do not
	// reach the network" is enforced by scripts/offline-test.sh, and a seam
	// that has to be honoured is better than one that has to be remembered.
	Fetch func(ctx context.Context) (io.ReadCloser, error)

	// Token is the bearer token for the default fetch. It never appears in a
	// log line or an error: the fetch reports the endpoint and the status, and
	// a 401 says the token is wrong without saying what it is.
	Token string

	// MaxAge is how old an observation may be before Admit reads upstream
	// again. Zero means every call fetches. It is a parameter and comes from
	// configuration.
	//
	// It is a floor on how often the endpoint is asked, not a ceiling on how
	// fresh the answer is: a worker pool polls its queue far faster than a
	// usage endpoint should be asked, and without this every pass of every
	// worker would be a request. The floor covers an attempt that failed as
	// well as one that answered - see due.
	MaxAge time.Duration

	// Threshold is the percentage of a window that counts as approaching its
	// limit. Zero is no threshold. A parameter, from configuration.
	Threshold float64

	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time

	// mu covers the cached observation and is held across the fetch, so that
	// several workers arriving at once make one request between them rather
	// than one each.
	mu sync.Mutex

	// last is the last observation that succeeded, and lastFail is when the
	// most recent attempt did not. They are separate because a failed fetch
	// leaves last standing on purpose (see Admit) - which means last's age says
	// nothing about how recently the endpoint was asked, and something has to.
	last     State
	lastFail time.Time
}

// Admit observes the budget and decides whether new work may start.
//
// The error is the fetch's, and it is reported rather than acted on: the
// decision always comes back usable. A fetch that failed leaves the last good
// observation standing, which is what keeps a deferral honest across an outage
// - an account known to be limited until 14:00 stays deferred to 14:00 even
// when the endpoint stops answering, rather than reverting to "unknown, so
// start" and spending the rest of the window failing at the provider.
func (o *Observer) Admit(ctx context.Context) (Admission, error) {
	state, err := o.Observe(ctx)
	return state.Admit(o.Threshold, o.now()), err
}

// Observe returns the budget as this process currently sees it, refreshing it
// if it is due.
//
// A failed refresh returns the last good observation alongside the error, and
// the zero State if there has never been one. The zero State is "not observed"
// and admits (see State.Admit).
func (o *Observer) Observe(ctx context.Context) (State, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := o.now()
	if !o.due(now) {
		return o.last, nil
	}

	state, err := o.fetch(ctx, now)
	if err != nil {
		o.lastFail = now
		return o.last, err
	}
	o.lastFail = time.Time{}
	o.last = state
	return state, nil
}

// due reports whether the cached observation must be replaced before it is
// used again.
func (o *Observer) due(now time.Time) bool {
	if o.MaxAge <= 0 {
		return true
	}
	if !o.lastFail.IsZero() && now.Before(o.lastFail.Add(o.MaxAge)) {
		// A failed attempt is throttled the same as a successful one, because a
		// failure leaves last unchanged and every case below would then be true
		// forever. Without this an endpoint that cannot be read - a rotated key
		// answering 401 is the ordinary way - is asked again for every job the
		// pool claims, which is the request rate MaxAge exists to hold down.
		//
		// It is the only thing here that outranks the reopened check: an
		// observation that cannot be refreshed is not made refreshable by
		// asking upstream more often.
		return false
	}
	switch {
	case !o.last.Known():
		return true
	case !now.Before(o.last.ObservedAt.Add(o.MaxAge)):
		return true
	case o.last.Limited() && !now.Before(o.last.ResetsAt()):
		// The window this observation said was limited has since reopened, so
		// what it says is known to be wrong whatever its age. Without this a
		// long MaxAge would hold the agent down past the reset it is waiting
		// for, which is the one moment freshness actually matters.
		return true
	}
	return false
}

func (o *Observer) fetch(ctx context.Context, now time.Time) (State, error) {
	get := o.Fetch
	if get == nil {
		get = o.httpFetch
	}
	rc, err := get(ctx)
	if err != nil {
		return State{}, err
	}
	defer rc.Close()
	return Decode(rc, now)
}

// httpFetch is the default Fetch: the usage endpoint, with the bearer token.
func (o *Observer) httpFetch(ctx context.Context) (io.ReadCloser, error) {
	return fetch.Get(ctx, Endpoint, o.Token)
}

func (o *Observer) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}
