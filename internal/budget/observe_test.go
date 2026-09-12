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

// An endpoint that cannot be read is asked once per maximum age, not once per
// job. A failed fetch leaves the last observation standing, so nothing about it
// ages - and without a floor of its own a rotated key answering 401 becomes one
// request and one log line for every job the pool claims, which is exactly the
// request rate the maximum age exists to hold down.
func TestAnUnreadableEndpointIsAskedOncePerMaxAge(t *testing.T) {
	var (
		down  = errors.New("https://opencode.ai/zen/go/v1/usage: 401 Unauthorized")
		calls int
		now   = at(t, "2026-09-11T12:00:00Z")
	)
	o := &budget.Observer{
		MaxAge: time.Minute,
		Now:    func() time.Time { return now },
		Fetch: func(context.Context) (io.ReadCloser, error) {
			calls++
			return nil, down
		},
	}

	adm, err := o.Admit(context.Background())
	if !errors.Is(err, down) {
		t.Fatalf("the fetch failure was not reported: %v", err)
	}
	if !adm.Starts() {
		t.Fatalf("an unreadable budget stopped work: %s", adm)
	}

	for range 9 {
		now = now.Add(5 * time.Second)
		adm, err := o.Admit(context.Background())
		// No error, because no request was made: the failure is reported by
		// the attempt that made it and not by every job that arrives while the
		// floor is in effect. A caller logging this gets one line an age too.
		if err != nil {
			t.Fatalf("reported %v without asking the endpoint", err)
		}
		if !adm.Starts() {
			t.Fatalf("an unreadable budget stopped work: %s", adm)
		}
	}
	if calls != 1 {
		t.Fatalf("asked the endpoint %d times inside one maximum age, want 1", calls)
	}

	now = now.Add(time.Minute)
	if _, err := o.Admit(context.Background()); !errors.Is(err, down) {
		t.Fatalf("the retry did not report the failure: %v", err)
	}
	if calls != 2 {
		t.Fatalf("asked the endpoint %d times, want a retry once the age has passed", calls)
	}
}

// A refresh that succeeds after a failure clears the floor, so a limited window
// reopening is still seen at the moment it reopens rather than an age later.
func TestASuccessfulRefreshClearsTheFailureFloor(t *testing.T) {
	var (
		down  = errors.New("dial tcp: no route to host")
		fails = true
		calls int
		now   = at(t, "2026-09-11T12:00:00Z")
		doc   = `{"usage":{"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T13:00:00Z"}}}`
	)
	o := &budget.Observer{
		MaxAge: 24 * time.Hour,
		Now:    func() time.Time { return now },
		Fetch: func(context.Context) (io.ReadCloser, error) {
			calls++
			if fails {
				return nil, down
			}
			return io.NopCloser(strings.NewReader(doc)), nil
		},
	}

	if _, err := o.Admit(context.Background()); !errors.Is(err, down) {
		t.Fatal(err)
	}
	fails = false
	now = now.Add(24 * time.Hour)
	if _, err := o.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The observation now says limited until 13:00 on the 11th, which is long
	// past: the reopened check must fire on the next look rather than waiting
	// out a 24-hour age it never restarted.
	doc = `{"usage":{"monthly":{"status":"ok","percent":4,"resetsAt":"2026-10-11T13:00:00Z"}}}`
	adm, err := o.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("asked the endpoint %d times, want a refresh at the reset", calls)
	}
	if !adm.Starts() {
		t.Fatalf("the window reopened and work did not resume: %s", adm)
	}
}
