package budget_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/corygyarmathy/afk-agent/internal/budget"
)

// The acceptance criterion: being limited defers work until resetsAt, an
// absolute timestamp, rather than polling and suppressing.
func TestLimitedDefersToTheResetTimestamp(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	adm := decode(t, live, now).Admit(0, now)

	if adm.Decision != budget.Defer {
		t.Fatalf("decision is %s, want defer", adm.Decision)
	}
	if want := at(t, "2026-09-27T00:00:00Z"); !adm.Until.Equal(want) {
		t.Fatalf("deferred until %s, want the window's own %s", adm.Until, want)
	}
	if adm.Starts() {
		t.Fatal("a limited budget admitted new work")
	}
}

// The acceptance criterion: approaching a limit stops new jobs starting. It is
// a wait rather than a deferral, because a rolling window's percent falls as
// old usage ages out of it - it can drop below the threshold well before it
// resets, and deferring to the reset would stand the agent down for hours it
// did not need to lose.
func TestApproachingALimitWaitsRatherThanDefers(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	const doc = `{"usage":{
		"rolling":{"status":"ok","percent":86,"resetsAt":"2026-09-11T18:00:00Z"},
		"weekly":{"status":"ok","percent":41,"resetsAt":"2026-09-15T00:00:00Z"}
	}}`
	s := decode(t, doc, now)

	adm := s.Admit(80, now)
	if adm.Decision != budget.Wait {
		t.Fatalf("decision is %s, want wait", adm.Decision)
	}
	if !adm.Until.IsZero() {
		t.Fatalf("a wait carries a timestamp %s; only a deferral has one", adm.Until)
	}
	if adm.Window.Name != "rolling" {
		t.Fatalf("the window that stopped the work is %q, want rolling", adm.Window.Name)
	}

	// The same observation, under a threshold it does not reach.
	if adm := s.Admit(90, now); !adm.Starts() {
		t.Fatalf("86%% stopped work under a 90%% threshold: %s", adm)
	}
	// And with no threshold at all, which is a coherent configuration: only an
	// actual limit stops work.
	if adm := s.Admit(0, now); !adm.Starts() {
		t.Fatalf("a budget with no threshold stopped work: %s", adm)
	}
}

// Limited with nothing to come back at waits rather than deferring. Deferring
// would push the job to a zero or past time, and a job scheduled for nothing
// waits for an operator rather than for the window to reopen.
func TestLimitedWithNoFutureResetWaits(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	for name, doc := range map[string]string{
		"no timestamp": `{"usage":{"monthly":{"status":"rate-limited","percent":100}}}`,
		"already past": `{"usage":{"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T11:00:00Z"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			adm := decode(t, doc, now).Admit(0, now)
			if adm.Decision != budget.Wait {
				t.Fatalf("decision is %s, want wait: a deferral here parks the job", adm.Decision)
			}
		})
	}
}

// An unobserved budget admits. That is the fail-open direction ADR 0001 §12
// already accepts: a budget that cannot be read is not a budget that is spent,
// and failing closed would let an outage of an undocumented endpoint stop the
// agent entirely.
func TestAnUnobservedBudgetAdmits(t *testing.T) {
	var none budget.State
	if none.Known() {
		t.Fatal("the zero state reports itself as an observation")
	}
	if adm := none.Admit(80, time.Now()); !adm.Starts() {
		t.Fatalf("an unobserved budget stopped work: %s", adm)
	}
}

// serve returns a Fetch that yields doc, and a counter of how often it was
// called.
func serve(doc *string) (func(context.Context) (io.ReadCloser, error), *int) {
	var calls int
	return func(context.Context) (io.ReadCloser, error) {
		calls++
		return io.NopCloser(strings.NewReader(*doc)), nil
	}, &calls
}

// An observation is reused for its maximum age. A worker pool polls its queue
// far faster than a usage endpoint should be asked, and without this every pass
// of every worker would be a request.
func TestObserverReusesAnObservationForItsMaxAge(t *testing.T) {
	doc := `{"usage":{"rolling":{"status":"ok","percent":10,"resetsAt":"2026-09-11T18:00:00Z"}}}`
	fetch, calls := serve(&doc)
	now := at(t, "2026-09-11T12:00:00Z")
	o := &budget.Observer{Fetch: fetch, MaxAge: time.Minute, Now: func() time.Time { return now }}

	for range 5 {
		if _, err := o.Observe(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != 1 {
		t.Fatalf("asked the endpoint %d times inside one maximum age", *calls)
	}

	now = now.Add(time.Minute)
	if _, err := o.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("asked the endpoint %d times, want a refresh once the age has passed", *calls)
	}
}

// A cached observation whose limited window has since reopened is known to be
// wrong whatever its age. Without this a long maximum age would hold the agent
// down past the reset it is waiting for, which is the one moment freshness
// actually matters.
func TestObserverRefreshesWhenTheLimitedWindowReopens(t *testing.T) {
	doc := `{"usage":{"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T13:00:00Z"}}}`
	fetch, calls := serve(&doc)
	now := at(t, "2026-09-11T12:00:00Z")
	o := &budget.Observer{Fetch: fetch, MaxAge: 24 * time.Hour, Now: func() time.Time { return now }}

	adm, err := o.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if adm.Decision != budget.Defer {
		t.Fatalf("decision is %s, want defer", adm.Decision)
	}

	doc = `{"usage":{"monthly":{"status":"ok","percent":4,"resetsAt":"2026-10-11T13:00:00Z"}}}`
	now = at(t, "2026-09-11T13:00:00Z")
	adm, err = o.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("asked the endpoint %d times, want a refresh at the reset", *calls)
	}
	if !adm.Starts() {
		t.Fatalf("the window reopened and work did not resume: %s", adm)
	}
}

// A failed refresh leaves the last good observation standing. An account known
// to be limited until 13:00 stays deferred to 13:00 even when the endpoint
// stops answering, rather than reverting to "unknown, so start" and spending
// the rest of the window failing at the provider.
func TestAFailedRefreshKeepsTheLastGoodObservation(t *testing.T) {
	doc := `{"usage":{"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T13:00:00Z"}}}`
	var (
		down  = errors.New("dial tcp: no route to host")
		fails bool
		calls int
		now   = at(t, "2026-09-11T12:00:00Z")
	)
	o := &budget.Observer{
		MaxAge: time.Minute,
		Now:    func() time.Time { return now },
		Fetch: func(context.Context) (io.ReadCloser, error) {
			calls++
			if fails {
				return nil, down
			}
			return io.NopCloser(strings.NewReader(doc)), nil
		},
	}

	if _, err := o.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}

	fails = true
	now = now.Add(2 * time.Minute)
	adm, err := o.Admit(context.Background())
	if !errors.Is(err, down) {
		t.Fatalf("the fetch failure was not reported: %v", err)
	}
	if adm.Decision != budget.Defer {
		t.Fatalf("decision is %s, want the last good observation's defer", adm.Decision)
	}
	if want := at(t, "2026-09-11T13:00:00Z"); !adm.Until.Equal(want) {
		t.Fatalf("deferred until %s, want %s", adm.Until, want)
	}
	if calls != 2 {
		t.Fatalf("the endpoint was asked %d times", calls)
	}
}

// A budget that has never been read admits, and says why it could not be read.
func TestAnObserverThatHasNeverSucceededAdmits(t *testing.T) {
	down := errors.New("401 Unauthorized")
	o := &budget.Observer{
		MaxAge: time.Minute,
		Fetch:  func(context.Context) (io.ReadCloser, error) { return nil, down },
	}

	adm, err := o.Admit(context.Background())
	if !errors.Is(err, down) {
		t.Fatalf("the fetch failure was not reported: %v", err)
	}
	if !adm.Starts() {
		t.Fatalf("an unreadable budget stopped work: %s", adm)
	}
}

// The observation the resolver takes is the same one admission takes. Two
// readings that could disagree is the thing this conversion exists to prevent:
// a job in flight when the window closes resolves against exactly what the
// pool saw.
func TestResolverBudgetIsTheSameObservation(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	s := decode(t, live, now)

	b := s.Resolver()
	if !b.Limited {
		t.Fatalf("the resolver's budget is not limited and the state is: %s", s)
	}
	if want := s.ResetsAt(); !b.ResetsAt.Equal(want) {
		t.Fatalf("resets at %s, want %s", b.ResetsAt, want)
	}

	// And an unlimited one carries no timestamp for the resolver to defer to.
	ok := decode(t, `{"usage":{"rolling":{"status":"ok","percent":3,"resetsAt":"2026-09-11T18:00:00Z"}}}`, now)
	if b := ok.Resolver(); b.Limited || !b.ResetsAt.IsZero() {
		t.Fatalf("an unlimited budget resolved as %+v", b)
	}
}
