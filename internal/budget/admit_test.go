package budget_test

import (
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

// A deferral names the window its timestamp came from, which is the one that
// reopens last and not the one nearest its limit. "defer until T: <window that
// reopens well before T>" reads as a bug in the agent rather than as the OR
// working, and the operator surface is the whole point of naming a window.
func TestADeferralNamesTheWindowItsTimestampCameFrom(t *testing.T) {
	now := at(t, "2026-09-11T12:00:00Z")
	// The peak is rolling, at 100%; the window that reopens last is monthly.
	const doc = `{"usage":{
		"rolling":{"status":"rate-limited","percent":100,"resetsAt":"2026-09-11T13:00:00Z"},
		"monthly":{"status":"rate-limited","percent":74,"resetsAt":"2026-09-27T00:00:00Z"}
	}}`
	s := decode(t, doc, now)

	if peak := s.Peak(); peak.Name != "rolling" {
		t.Fatalf("the peak window is %q, want rolling: the test needs them to differ", peak.Name)
	}
	adm := s.Admit(0, now)
	if adm.Decision != budget.Defer {
		t.Fatalf("decision is %s, want defer", adm.Decision)
	}
	if adm.Window.Name != "monthly" {
		t.Fatalf("the deferral names %q, want the window that reopens last", adm.Window.Name)
	}
	if !adm.Window.ResetsAt.Equal(adm.Until) {
		t.Fatalf("deferred until %s while naming a window that resets at %s", adm.Until, adm.Window.ResetsAt)
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
